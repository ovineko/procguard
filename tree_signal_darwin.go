//go:build darwin

package procguard

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func (t *processTree) signal(sig syscall.Signal) error {
	err := syscall.Kill(-t.pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) {
		// Darwin can report EPERM for a group containing only exited members.
		// Include the leader: an unsignalable live leader is a real failure.
		processes, probeErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", t.pid)
		if probeErr == nil && !darwinGroupHasLiveProcesses(processes, t.pid, false) {
			return nil
		}
		if probeErr != nil {
			err = errors.Join(err, fmt.Errorf("confirm process group exit: %w", probeErr))
		}
	}
	return fmt.Errorf("signal process group: %w", err)
}
