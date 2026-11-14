//go:build !windows && !darwin

package procguard

import (
	"errors"
	"fmt"
	"syscall"
)

func (t *processTree) signal(sig syscall.Signal) error {
	if err := syscall.Kill(-t.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal process group: %w", err)
	}
	return nil
}
