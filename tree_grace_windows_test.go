//go:build windows

package procguard

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsDescendantGrace(t *testing.T) {
	child, grand, _, gate := startCleanupTree(t, "ignore", "natural", 500*time.Millisecond)
	//nolint:gosec // The fixture reports an OS-assigned process ID.
	member, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(grand))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(member) })
	inJob, err := processInJob(member, child.tree.job)
	if err != nil || !inJob {
		_ = windows.TerminateProcess(member, 1)
		t.Fatalf("descendant %d belongs to assigned job=%t err=%v", grand, inJob, err)
	}
	started := time.Now()
	if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if exit.Err != nil || !exit.Killed || exit.Requested || exit.Code != 0 {
		var status uint32
		queryErr := windows.GetExitCodeProcess(member, &status)
		t.Fatalf("exit: %+v; descendant %d status=%d query_err=%v", exit, grand, status, queryErr)
	}
	if elapsed := time.Since(started); elapsed < testScale(500*time.Millisecond) || elapsed >= testScale(2*time.Second) {
		t.Fatalf("job grace duration: %v", elapsed)
	}
	eventually(t, func() bool { return processGone(t, grand) })
}
