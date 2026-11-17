//go:build linux

package procguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func (t *processTree) remaining() (bool, error) {
	// A cgroup launch answers from the leaf: membership is kernel-maintained,
	// so a setsid escape or an orphaned descendant still counts.
	if t.cg != nil {
		return t.cg.remaining()
	}
	return remainingInProc("/proc", t.pid, os.ReadFile, unix.Getpgid)
}

func remainingInProc(root string, leader int, readFile func(string) ([]byte, error), getpgid func(int) (int, error)) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("enumerate process group: %w", err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == leader {
			continue
		}
		data, err := readFile(filepath.Join(root, entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if errors.Is(err, os.ErrPermission) {
			group, groupErr := getpgid(pid)
			if errors.Is(groupErr, unix.ESRCH) || (groupErr == nil && group != leader) {
				continue
			}
		}
		if err != nil {
			return false, fmt.Errorf("read process group member: %w", err)
		}
		group, state, err := parseProcessStat(string(data))
		if err != nil {
			actualGroup, groupErr := getpgid(pid)
			switch {
			case errors.Is(groupErr, unix.ESRCH):
				continue
			case groupErr != nil:
				return false, fmt.Errorf("inspect process %d in group %d: %w", pid, leader,
					errors.Join(err, fmt.Errorf("get process group: %w", groupErr)))
			case actualGroup < 0, actualGroup == leader:
				return false, fmt.Errorf("inspect process %d in group %d: %w", pid, leader, err)
			default:
				continue
			}
		}
		if group == leader && state != 'Z' && state != 'X' && state != 'x' {
			return true, nil
		}
	}
	return false, nil
}

func parseProcessStat(stat string) (int, byte, error) {
	// comm is unescaped and can contain spaces and parentheses. Only the last
	// closing parenthesis separates it from the fixed state/ppid/pgrp fields.
	end := strings.LastIndexByte(stat, ')')
	start := strings.Index(stat, " (")
	if start < 1 || end < start+2 {
		return 0, 0, errors.New("process stat has no closing parenthesis")
	}
	pid, pidErr := strconv.ParseInt(stat[:start], 10, 32)
	if pidErr != nil || pid <= 0 || stat[0] < '0' || stat[0] > '9' {
		return 0, 0, errors.New("process stat has invalid PID")
	}
	if end+1 >= len(stat) || stat[end+1] != ' ' {
		return 0, 0, errors.New("process stat has invalid comm delimiter")
	}
	fields := strings.SplitN(strings.TrimSuffix(stat[end+2:], "\n"), " ", 4)
	if len(fields) < 3 || len(fields[0]) != 1 || !strings.ContainsAny(fields[0], "RSDZTtXxKWPI") {
		return 0, 0, errors.New("process stat has invalid state/ppid/pgrp fields")
	}
	parent, parentErr := strconv.ParseInt(fields[1], 10, 32)
	if parentErr != nil || parent < 0 || fields[1][0] < '0' || fields[1][0] > '9' {
		return 0, 0, errors.New("process stat has invalid parent PID")
	}
	group, err := strconv.ParseInt(fields[2], 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("parse process group: %w", err)
	}
	if group < 0 || fields[2][0] < '0' || fields[2][0] > '9' {
		return 0, 0, errors.New("process stat has invalid process group")
	}
	return int(group), fields[0][0], nil
}
