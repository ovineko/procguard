//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package procguard

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

type processTree struct {
	pid, queue int
	exited     bool
}

func newProcessTree(pid int, _ *cgroupResources) (*processTree, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("create exit observer: %w", err)
	}
	unix.CloseOnExec(queue)
	var event unix.Kevent_t
	unix.SetKevent(&event, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	event.Fflags = unix.NOTE_EXIT
	_, err = unix.Kevent(queue, []unix.Kevent_t{event}, nil, nil)
	if err != nil && !errors.Is(err, unix.ESRCH) {
		_ = unix.Close(queue)
		return nil, fmt.Errorf("register child exit: %w", err)
	}
	return &processTree{pid: pid, queue: queue, exited: errors.Is(err, unix.ESRCH)}, nil
}

func (t *processTree) close() error {
	if err := unix.Close(t.queue); err != nil {
		return fmt.Errorf("close exit observer: %w", err)
	}
	return nil
}

func (t *processTree) kill() error { return t.signal(syscall.SIGKILL) }

func (t *processTree) waitExited() error {
	if t.exited {
		return nil
	}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(t.queue, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("observe child exit: %w", err)
		}
		if n > 0 {
			return nil
		}
	}
}
