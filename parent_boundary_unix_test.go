//go:build !windows

package procguard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWatchParentIntervalEndpoints(t *testing.T) {
	parent, cancelParent := context.WithCancel(t.Context())
	cancelParent()
	for _, interval := range []time.Duration{500 * time.Millisecond, 60 * time.Second} {
		ctx, cancel, err := WatchParent(parent, 1, interval)
		if err != nil {
			t.Fatalf("interval=%v: %v", interval, err)
		}
		cancel()
		if !errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatalf("parent cancellation lost: %v", context.Cause(ctx))
		}
	}
}
