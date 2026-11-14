//go:build !windows

package procguard

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchParentPolling(t *testing.T) {
	for _, action := range []string{"reparent", "cancel", "parent-cancel", "parent-cause", "initial-race"} {
		t.Run(action, func(t *testing.T) {
			original := getppidFunc
			t.Cleanup(func() { getppidFunc = original })
			var current atomic.Int32
			current.Store(123)
			if action == "initial-race" {
				current.Store(1)
			}
			checked := make(chan struct{}, 10)
			getppidFunc = func() int {
				select {
				case checked <- struct{}{}:
				default:
				}
				return int(current.Load())
			}
			parent, parentCancel := context.WithCancelCause(t.Context())
			defer parentCancel(nil)
			wantCause := context.Canceled
			ctx, cancel, err := WatchParent(parent, 123, minPollInterval)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			select {
			case <-checked:
			case <-time.After(testDeadline):
				t.Fatal("no initial check")
			}
			switch action {
			case "reparent":
				if ctx.Err() != nil {
					t.Fatalf("watch cancelled before parent changed: %v", context.Cause(ctx))
				}
				current.Store(1)
			case "cancel":
				cancel()
			case "parent-cancel":
				parentCancel(nil)
			case "parent-cause":
				wantCause = errors.New("consumer shutdown")
				parentCancel(wantCause)
			}
			select {
			case <-ctx.Done():
			case <-time.After(testDeadline):
				t.Fatal("monitor did not cancel")
			}
			var pe *ParentExitError
			cause := context.Cause(ctx)
			if action == "reparent" || action == "initial-race" {
				if !errors.As(cause, &pe) || pe.ExpectedPID != 123 || pe.ObservedPID != 1 {
					t.Fatalf("parent exit cause: %#v", cause)
				}
			} else if errors.As(cause, &pe) || !errors.Is(cause, wantCause) {
				t.Fatalf("cancellation cause: %v, want %v", cause, wantCause)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error: %v", ctx.Err())
			}
		})
	}
}
