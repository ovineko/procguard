//go:build !windows

package procguard

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func (t *processTree) resume() error    { return nil }
func (t *processTree) terminate() error { return t.signal(syscall.SIGTERM) }

func (t *processTree) signal(sig syscall.Signal) error {
	if err := syscall.Kill(-t.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal process group: %w", err)
	}
	return nil
}

func exitSignal(state *os.ProcessState) os.Signal {
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return status.Signal()
		}
	}
	return nil
}

func killUnattached(process *os.Process) error {
	tree := processTree{pid: process.Pid}
	killErr := tree.kill()
	_, _, cleanupErr := cleanProcessTree(&tree, 0, true, time.Time{}, time.Time{}, true, killErr)
	return errors.Join(killErr, cleanupErr)
}
