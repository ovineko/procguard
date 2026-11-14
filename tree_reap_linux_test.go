//go:build linux

package procguard

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"golang.org/x/sys/unix"
)

func TestDescendantKillReaped(t *testing.T) {
	if !helperEnabled() {
		ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
		defer cancel()
		// #nosec G204 G702 -- Re-executes this test binary with a fixed test selector.
		cmd := exec.CommandContext(ctx, testsupport.Executable(), "-test.run=^TestDescendantKillReaped$")
		cmd.Env = helperEnv()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("subreaper test: %v\n%s", err, output)
		}
		return
	}
	// Isolate adoption from the runner so this test never reaps another test's child.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	child, grand, _, _ := startCleanupTree(t, "ignore", "stop", 500*time.Millisecond)
	child.Stop()
	if exit := awaitExit(t, child); !exit.Killed || exit.Err != nil {
		t.Fatalf("exit: %+v", exit)
	}
	var status unix.WaitStatus
	eventually(t, func() bool {
		pid, err := unix.Wait4(grand, &status, unix.WNOHANG, nil)
		if err != nil {
			t.Fatal(err)
		}
		return pid == grand
	})
	if !status.Signaled() || status.Signal() != unix.SIGKILL {
		t.Fatalf("descendant status: %v", status)
	}
	if err := unix.Kill(grand, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("descendant still exists: %v", err)
	}
}
