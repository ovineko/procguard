//go:build !linux && !windows

package procguard

import (
	"context"
	"os"
	"time"
)

var getppidFunc = os.Getppid

func watchParent(parent context.Context, pid int, interval time.Duration) (context.Context, context.CancelFunc, error) {
	ctx, cancelCause := context.WithCancelCause(parent)
	cancel := func() { cancelCause(nil) }
	getppid := getppidFunc
	if observed := getppid(); observed != pid {
		cancelCause(&ParentExitError{ExpectedPID: pid, ObservedPID: observed})
		return ctx, cancel, nil
	}
	go func() {
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
	return ctx, cancel, nil
}
