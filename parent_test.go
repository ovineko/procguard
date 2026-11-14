package procguard

import (
	"testing"
	"time"
)

func TestWatchParentValidation(t *testing.T) {
	for _, tc := range []struct {
		pid      int
		interval time.Duration
	}{{0, 0}, {-1, 0}, {1, time.Millisecond}, {1, 61 * time.Second}} {
		ctx, cancel, err := WatchParent(t.Context(), tc.pid, tc.interval)
		if cancel != nil {
			cancel()
		}
		if err == nil || ctx != nil {
			t.Errorf("invalid config accepted: %+v", tc)
		}
	}
}
