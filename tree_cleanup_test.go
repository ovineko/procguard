//go:build linux || darwin || windows

package procguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startCleanupTree(t *testing.T, mode, action string, grace time.Duration) (*Child, int, string, string) {
	t.Helper()
	dir := t.TempDir()
	gate, ready, marker := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker")
	exitGate := "stop"
	if action == "natural" {
		exitGate = filepath.Join(dir, "exit")
	}
	var out observedOutput
	spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, mode)
	spec.GracePeriod = testScale(grace)
	spec.Stdout, spec.Stderr = &out, &out
	spec.AfterStart = func(_ int, err error) {
		if err == nil {
			if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	child := startHelper(t, spec)
	awaitReady(t, &out)
	pid, err := strconv.Atoi(strings.TrimPrefix(strings.Split(out.String(), "\n")[0], "grand:"))
	if err != nil {
		t.Fatal(err)
	}
	return child, pid, marker, exitGate
}

func TestImmediateNaturalExitKillsDescendant(t *testing.T) {
	dir := t.TempDir()
	gate, ready, marker, exitGate := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
	var out observedOutput
	spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, "graceful")
	spec.GracePeriod = testScale(30 * time.Second)
	spec.KillImmediately = true
	spec.Stdout, spec.Stderr = &out, &out
	spec.AfterStart = func(_ int, err error) {
		if err == nil {
			if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	child := startHelper(t, spec)
	awaitReady(t, &out)
	pid, err := strconv.Atoi(strings.TrimPrefix(strings.Split(out.String(), "\n")[0], "grand:"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if exit.Err != nil || !exit.Killed || exit.Requested {
		t.Fatalf("exit = %+v", exit)
	}
	eventually(t, func() bool { return processGone(t, pid) })
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant cleanup marker exists: %v", err)
	}
}
