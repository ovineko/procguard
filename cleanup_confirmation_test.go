package procguard

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	errInjectedCleanupProbe = errors.New("injected cleanup probe failure")
	errInjectedCleanupKill  = errors.New("injected cleanup kill failure")
)

type observedFaultTree struct {
	cleanupTree

	probeErr error
	killErr  error
	mu       sync.Mutex
	probes   []bool
}

func (t *observedFaultTree) remaining() (bool, error) {
	remaining, err := t.cleanupTree.remaining()
	t.mu.Lock()
	t.probes = append(t.probes, remaining)
	t.mu.Unlock()
	if t.probeErr != nil {
		return remaining, errors.Join(err, t.probeErr)
	}
	return remaining, err
}

func (t *observedFaultTree) kill() error {
	if t.killErr != nil {
		return t.killErr
	}
	return t.cleanupTree.kill()
}

func (t *observedFaultTree) sawLiveMember() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, remaining := range t.probes {
		if remaining {
			return true
		}
	}
	return false
}

func cleanupTreeHooks(wrap func(cleanupTree) cleanupTree) launchHooks {
	return launchHooks{cleanup: wrap}
}

func cleanupTreeSpec(t *testing.T, output *observedOutput) (Spec, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	launchGate := dir + "/launch"
	memberReady := dir + "/member.ready"
	memberDone := dir + "/member.done"
	leaderGate := dir + "/leader.exit"
	helperWriteFile(launchGate, "go")
	spec := helperSpec("cleanup-tree", launchGate, memberReady, memberDone, leaderGate, "graceful")
	spec.Stdout = output
	spec.KillImmediately = true
	return spec, memberReady, memberDone, leaderGate
}

func cleanupDescendantPID(t *testing.T, output *observedOutput) int {
	t.Helper()
	for line := range strings.SplitSeq(output.String(), "\n") {
		if after, ok := strings.CutPrefix(line, "grand:"); ok {
			pid, err := strconv.Atoi(after)
			if err != nil || pid <= 0 {
				t.Fatalf("invalid descendant pid line %q: %v", line, err)
			}
			return pid
		}
	}
	t.Fatalf("helper did not report descendant PID: %q", output.String())
	return 0
}

func releaseCleanupDescendant(t *testing.T, pid int) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		eventually(t, func() bool { return processGone(t, pid) })
		return false
	}
	if processGone(t, pid) {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find still-live descendant %d: %v", pid, err)
	}
	defer func() { _ = process.Release() }()
	// The test helper handles SIGTERM by leaving its wait gate and recording
	// completion. This addresses the observed live PID, never a saved PGID.
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if killErr := process.Kill(); killErr != nil {
			t.Fatalf("release descendant %d: signal: %v; kill: %v", pid, err, killErr)
		}
		return false
	}
	return true
}

func awaitMarker(t *testing.T, path string) {
	t.Helper()
	eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

var timeoutForCleanupTest = testScale(5 * time.Second)
