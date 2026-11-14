//go:build e2e && !linux && !windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func processState(pid int) (bool, bool, error) {
	//nolint:noctx // Observer probes must remain independent of the consumer context being cancelled.
	// #nosec G204 -- ps receives a fixed option list and an OS-assigned numeric PID, without a shell.
	data, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(data))
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && state == "" && len(exit.Stderr) == 0 {
			return false, false, nil
		}
		return false, false, fmt.Errorf("ps: %w", err)
	}
	zombie := strings.HasPrefix(state, "Z")
	return state != "" && !zombie, zombie, nil
}
