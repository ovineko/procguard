//go:build linux || darwin

package procguard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExitedPrecedesDescendantCleanup(t *testing.T) {
	child, _, marker, exitGate := startCleanupTree(t, "graceful", "natural", 5*time.Second)
	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.Exited():
	case <-time.After(testDeadline):
		t.Fatal("leader exit was not observed")
	}
	select {
	case <-child.Done():
		t.Fatal("Done closed before the descendant finished its graceful cleanup")
	default:
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("descendant finished before the leader exit was reported: %v", err)
	}
	awaitExit(t, child)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("descendant cleanup marker: %v", err)
	}
}

func TestSupervisorStateIsNotRunningDuringDescendantCleanup(t *testing.T) {
	dir := t.TempDir()
	gate, ready, marker, exitGate := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
	var out observedOutput
	spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, "graceful")
	spec.GracePeriod = testScale(5 * time.Second)
	spec.Stdout, spec.Stderr = &out, &out
	spec.AfterStart = func(_ int, err error) {
		if err == nil {
			if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	s, results := runSupervisor(t.Context(), t, Options{
		Spec:    func(context.Context) (Spec, error) { return spec, nil },
		Restart: RestartPolicy{Mode: RestartNever},
	})
	awaitReady(t, &out)
	if !s.State().Running {
		t.Fatal("State.Running = false while the leader is alive")
	}
	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !s.State().Running })
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("descendant finished before Running turned false: %v", err)
	}
	outcome := awaitOutcome(t, results)
	if outcome.Reason != Completed || outcome.Last == nil || outcome.Last.Code != 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("descendant cleanup marker: %v", err)
	}
}
