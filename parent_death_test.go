//go:build linux || windows

package procguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParentDeath(t *testing.T) {
	var out observedOutput
	ready := readyPath(t)
	report := filepath.Join(t.TempDir(), "cause")
	spec := helperSpec("supervisor", ready)
	if runtime.GOOS == "linux" {
		spec = helperSpec("supervisor", ready, report)
	}
	// #nosec G204 G702 -- helperSpec re-executes this test binary with test-owned arguments.
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	eventually(t, func() bool { return strings.Contains(out.String(), "child:") })
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(out.String(), "child:")))
	if err != nil {
		t.Fatal(err)
	}
	if processGone(t, pid) {
		t.Fatal("child exited before parent death")
	}
	if err := cmd.Process.Signal(os.Kill); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return processGone(t, pid) })
	if runtime.GOOS == "linux" {
		// #nosec G304 -- report is a fixed filename inside t.TempDir.
		data, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		var expected, observed int
		if _, err := fmt.Sscanf(string(data), "%d %d", &expected, &observed); err != nil {
			t.Fatal(err)
		}
		if expected != cmd.Process.Pid || observed == expected || observed <= 0 {
			t.Fatalf("parent exit cause: expected=%d observed=%d, parent=%d", expected, observed, cmd.Process.Pid)
		}
	}
}
