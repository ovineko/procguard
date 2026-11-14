//go:build openbsd

package procguard

import (
	"math"

	"golang.org/x/sys/unix"
)

func (t *processTree) remaining() (bool, error) {
	return bsdGroupRemaining(t.pid, func() ([]byte, error) {
		return unix.SysctlRaw("kern.proc", bsdProcPgrp, t.pid, bsdRecordSize, math.MaxInt32)
	})
}
