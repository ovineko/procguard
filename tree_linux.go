//go:build linux

package procguard

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// processTree tracks the termination unit. A cgroup-backed launch carries
// the leaf handle: membership and hard kills then come from the cgroup,
// which no setsid escape can leave.
type processTree struct {
	pid int
	cg  *cgroupResources
}

func newProcessTree(pid int, res *cgroupResources) (*processTree, error) {
	return &processTree{pid: pid, cg: res}, nil
}

func (t *processTree) close() error {
	if t.cg != nil {
		return t.cg.close()
	}
	return nil
}

// kill empties the unit. A cgroup launch prefers the atomic leaf kill — it
// also covers descendants that escaped the process group — and falls back
// to the group signal if the leaf write fails.
func (t *processTree) kill() error {
	if t.cg != nil {
		if err := t.cg.kill(); err != nil {
			return errors.Join(err, t.signal(syscall.SIGKILL))
		}
		return nil
	}
	return t.signal(syscall.SIGKILL)
}

func (t *processTree) waitExited() error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, t.pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("observe child exit: %w", err)
		}
		return nil
	}
}
