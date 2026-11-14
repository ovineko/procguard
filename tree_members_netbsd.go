//go:build netbsd

package procguard

import (
	"math"

	"golang.org/x/sys/unix"
)

func (t *processTree) remaining() (bool, error) {
	return bsdGroupRemaining(t.pid, func() ([]byte, error) {
		return unix.SysctlRaw("kern.proc2", bsdProcPgrp, t.pid, bsdRecordSize, math.MaxInt32)
	})
}
