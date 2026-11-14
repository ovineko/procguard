//go:build dragonfly

package procguard

import "golang.org/x/sys/unix"

func (t *processTree) remaining() (bool, error) {
	return bsdGroupRemaining(t.pid, func() ([]byte, error) {
		return unix.SysctlRaw("kern.proc.pgrp", t.pid)
	})
}
