//go:build linux || darwin

package procguard

import (
	"os"
	"testing"
	"time"
)

func TestDescendantGrace(t *testing.T) {
	for _, action := range []string{"stop", "natural"} {
		t.Run(action, func(t *testing.T) {
			child, grand, marker, gate := startCleanupTree(t, "graceful", action, 5*time.Second)
			started := time.Now()
			switch action {
			case "natural":
				if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				child.Stop()
			}
			exit := awaitExit(t, child)
			if exit.Err != nil || exit.Killed || exit.Code != 0 || exit.Requested != (action != "natural") {
				t.Fatalf("exit: %+v", exit)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("descendant cleanup did not complete: %v", err)
			}
			if elapsed := time.Since(started); elapsed < testScale(300*time.Millisecond) || elapsed >= testScale(2*time.Second) {
				t.Fatalf("cleanup duration: %v", elapsed)
			}
			eventually(t, func() bool { return processGone(t, grand) })
		})
	}
}

func TestDescendantEscalation(t *testing.T) {
	for _, action := range []string{"stop", "natural"} {
		t.Run(action, func(t *testing.T) {
			child, grand, _, gate := startCleanupTree(t, "ignore", action, 500*time.Millisecond)
			started := time.Now()
			if action == "stop" {
				child.Stop()
			} else if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
				t.Fatal(err)
			}
			exit := awaitExit(t, child)
			if exit.Err != nil || !exit.Killed || exit.Requested != (action == "stop") {
				t.Fatalf("exit: %+v", exit)
			}
			if elapsed := time.Since(started); elapsed < testScale(500*time.Millisecond) || elapsed >= testScale(2*time.Second) {
				t.Fatalf("escalation duration: %v", elapsed)
			}
			eventually(t, func() bool { return processGone(t, grand) })
		})
	}
}
