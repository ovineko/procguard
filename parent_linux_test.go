//go:build linux

package procguard

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWatchParentLinux(t *testing.T) {
	for _, tc := range []struct {
		name      string
		actual    int
		setupErr  error
		cancelled bool
	}{
		{"success", 123, nil, false}, {"race", 1, nil, true}, {"prctl-error", 123, os.ErrPermission, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalPrctl, originalPPID := prctlFunc, getppidFunc
			t.Cleanup(func() { prctlFunc, getppidFunc = originalPrctl, originalPPID })
			cleared := make(chan struct{})
			var setupThread int
			prctlFunc = func(option int, arg2, arg3, arg4, arg5 uintptr) error {
				if option != unix.PR_SET_PDEATHSIG || (arg2 != 0 && arg2 != uintptr(unix.SIGTERM)) || arg3 != 0 || arg4 != 0 || arg5 != 0 {
					t.Error("wrong prctl arguments")
				}
				if arg2 != 0 {
					setupThread = unix.Gettid()
				} else {
					if unix.Gettid() != setupThread {
						t.Error("PDEATHSIG cleared on a different OS thread")
					}
					close(cleared)
				}
				return tc.setupErr
			}
			getppidFunc = func() int { return tc.actual }
			ctx, cancel, err := WatchParent(t.Context(), 123, 0)
			if tc.setupErr != nil {
				if !errors.Is(err, tc.setupErr) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			t.Cleanup(func() {
				select {
				case <-cleared:
				case <-time.After(testDeadline):
					t.Error("PDEATHSIG was not cleared")
				}
			})
			if (ctx.Err() != nil) != tc.cancelled {
				t.Fatalf("cancelled: %v", ctx.Err())
			}
			cancel()
			select {
			case <-ctx.Done():
			case <-time.After(testDeadline):
				t.Fatal("cancel did not propagate")
			}
		})
	}
}

func TestWatchParentRealSignal(t *testing.T) {
	if os.Getppid() <= 0 {
		t.Skip("no visible parent (PID 1 in a PID namespace); use --init to exercise parent watching")
	}
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, unix.SIGTERM)
	defer signal.Stop(terms)

	original := getppidFunc
	t.Cleanup(func() { getppidFunc = original })
	checked := make(chan struct{}, 1)
	getppidFunc = func() int {
		select {
		case checked <- struct{}{}:
		default:
		}
		return os.Getppid()
	}
	ctx, cancel, err := WatchParent(t.Context(), os.Getppid(), minPollInterval)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-checked:
	case <-time.After(testDeadline):
		t.Fatal("no initial parent check")
	}
	if err := unix.Kill(os.Getpid(), unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terms:
	case <-time.After(testDeadline):
		t.Fatal("consumer did not receive SIGTERM")
	}
	for range 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("SIGTERM cancelled watch: %v", context.Cause(ctx))
		case <-checked:
		case <-time.After(testDeadline):
			t.Fatal("watch did not keep polling after SIGTERM")
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("SIGTERM cancelled watch: %v", context.Cause(ctx))
	}
}
