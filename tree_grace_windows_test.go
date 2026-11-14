//go:build windows

package procguard

import (
	"os"
	"testing"
	"time"
)

func TestWindowsDescendantGrace(t *testing.T) {
	child, grand, _, gate := startCleanupTree(t, "ignore", "natural", 500*time.Millisecond)
	started := time.Now()
	if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if exit.Err != nil || !exit.Killed || exit.Requested || exit.Code != 0 {
		t.Fatalf("exit: %+v", exit)
	}
	if elapsed := time.Since(started); elapsed < testScale(500*time.Millisecond) || elapsed >= testScale(2*time.Second) {
		t.Fatalf("job grace duration: %v", elapsed)
	}
	eventually(t, func() bool { return processGone(t, grand) })
}
