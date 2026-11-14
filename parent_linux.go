//go:build linux

package procguard

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

var (
	prctlFunc   = unix.Prctl
	getppidFunc = os.Getppid
)

func watchParent(parent context.Context, pid int, interval time.Duration) (context.Context, context.CancelFunc, error) {
	ctx, cancelCause := context.WithCancelCause(parent)
	cancel := func() { cancelCause(nil) }
	ready := make(chan error, 1)
	// Capture test seams before starting the goroutine so restoring them cannot
	// race its teardown.
	prctl, getppid := prctlFunc, getppidFunc
	go func() {
		// PDEATHSIG is per thread and is lost when that thread exits, so keep
		// this dedicated goroutine's OS thread locked for the watch's lifetime.
		// The kernel delivers SIGTERM to the process; consumers own its handling.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0); err != nil {
			ready <- fmt.Errorf("set parent death signal: %w", err)
			return
		}
		defer func() { _ = prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0) }()
		if observed := getppid(); observed != pid {
			cancelCause(&ParentExitError{ExpectedPID: pid, ObservedPID: observed})
		}
		ready <- nil
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if observed := getppid(); observed != pid {
					cancelCause(&ParentExitError{ExpectedPID: pid, ObservedPID: observed})
					return
				}
			}
		}
	}()
	if err := <-ready; err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}
