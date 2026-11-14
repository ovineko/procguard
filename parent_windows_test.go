//go:build windows

package procguard

import (
	"errors"
	"testing"
)

func TestWatchParentWindows(t *testing.T) {
	ctx, cancel, err := WatchParent(t.Context(), 123, 0)
	if cancel != nil {
		cancel()
	}
	if ctx != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
