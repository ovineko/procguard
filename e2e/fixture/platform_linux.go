//go:build e2e && linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"golang.org/x/sys/unix"
)

func cgroupDelegationDenied(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, unix.EROFS)
}

func processState(pid int) (bool, bool, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read process state: %w", err)
	}
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) < 1 {
		return false, false, errors.New("malformed process stat")
	}
	zombie := fields[0] == "Z" || fields[0] == "X"
	return !zombie, zombie, nil
}

func subreaper() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set subreaper: %w", err)
	}
	return nil
}

func reapOrphans() {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err != nil || pid <= 0 {
			return
		}
	}
}

func resources() (int, int, error) {
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, 0, fmt.Errorf("count fds: %w", err)
	}
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return 0, 0, fmt.Errorf("count threads: %w", err)
	}
	return len(fds), len(threads), nil
}

func inheritedOOM() (int, error) {
	data, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		return 0, fmt.Errorf("read inherited OOM score: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse OOM score: %w", err)
	}
	return n, nil
}

func limitValues() (wire.LimitObservation, error) { return childLimitValues(os.Getpid()) }
func childLimitValues(pid int) (wire.LimitObservation, error) {
	var r wire.LimitObservation
	path := filepath.Join("/proc", strconv.Itoa(pid))
	// #nosec G304 -- The path is a fixed procfs filename under an OS-assigned numeric PID.
	data, err := os.ReadFile(filepath.Join(path, "oom_score_adj"))
	if err != nil {
		return r, fmt.Errorf("read OOM: %w", err)
	}
	r.OOM, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return r, fmt.Errorf("parse OOM: %w", err)
	}
	// #nosec G304 -- The path is a fixed procfs filename under an OS-assigned numeric PID.
	data, err = os.ReadFile(filepath.Join(path, "limits"))
	if err != nil {
		return r, fmt.Errorf("read limits: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "Max address space") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 6 {
			return r, errors.New("malformed address-space limits")
		}
		r.AddressSpace, err = parseLimit(fields[3])
		if err != nil {
			return r, err
		}
		r.HardAddressSpace, err = parseLimit(fields[4])
		return r, err
	}
	return r, errors.New("missing address-space limits")
}

func parseLimit(value string) (uint64, error) {
	if value == "unlimited" {
		return ^uint64(0), nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse limit: %w", err)
	}
	return n, nil
}
