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
	for _, process := range processes {
		if int(process.Proc.P_pid) != t.pid && int(process.Eproc.Pgid) == t.pid && process.Proc.P_stat != darwinZombie {
			return true, nil
		}
	}
	return false, nil
}
