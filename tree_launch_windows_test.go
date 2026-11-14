//go:build windows

package procguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
)

func TestWindowsImmediateGrandchild(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out observedOutput
	spec := helperSpec("tree", gate, filepath.Join(dir, "ready"))
	spec.Stdout = &out
	child := startHelper(t, spec)
	awaitReady(t, &out)
	grand, err := strconv.Atoi(strings.TrimPrefix(strings.Split(out.String(), "\n")[0], "grand:"))
	if err != nil {
		t.Fatal(err)
	}
	child.Stop()
	awaitExit(t, child)
	if !gonewithin(t, grand) {
		process, err := os.FindProcess(grand)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = process.Release() }()
		_ = process.Kill()
		t.Fatalf("grandchild %d survived job stop", grand)
	}
}

func gonewithin(t *testing.T, pid int) bool {
	t.Helper()
	deadline := time.Now().Add(testsupport.Scale(2 * time.Second))
	for time.Now().Before(deadline) {
		if processGone(t, pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return processGone(t, pid)
}
