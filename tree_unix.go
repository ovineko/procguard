//go:build !windows

package procguard

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func (t *processTree) resume() error    { return nil }
func (t *processTree) terminate() error { return t.signal(syscall.SIGTERM) }

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
