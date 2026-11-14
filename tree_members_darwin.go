//go:build darwin

package procguard

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Darwin's sys/proc.h defines SZOMB; x/sys exposes P_stat but not this constant.
const darwinZombie = 5

func (t *processTree) remaining() (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", t.pid)
	if err != nil {
		return false, fmt.Errorf("enumerate process group: %w", err)
	}
	return darwinGroupHasLiveProcesses(processes, t.pid, true), nil
}

func darwinGroupHasLiveProcesses(processes []unix.KinfoProc, pgid int, excludeLeader bool) bool {
	for _, process := range processes {
		if excludeLeader && int(process.Proc.P_pid) == pgid {
			continue
		}
		if int(process.Eproc.Pgid) == pgid && process.Proc.P_stat != darwinZombie {
			return true
		}
	}
	return false
}
