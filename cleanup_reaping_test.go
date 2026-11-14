package procguard

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type reapingBarrierTree struct {
	cleanupTree

	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (tree *reapingBarrierTree) remaining() (bool, error) {
	tree.once.Do(func() {
		close(tree.entered)
		<-tree.release
	})
	return false, nil
}

func TestCleanupUnconfirmedWithoutOwnedLeaderReaping(t *testing.T) {
	dir := t.TempDir()
	ready, leaderGate := filepath.Join(dir, "ready"), filepath.Join(dir, "exit")
	barrier := &reapingBarrierTree{entered: make(chan struct{}), release: make(chan struct{})}
	child, err := startWith(context.Background(), helperSpec("gated-exit", ready, leaderGate, "0"), launchHooks{
		cleanup: func(underlying cleanupTree) cleanupTree {
			barrier.cleanupTree = underlying
			return barrier
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	t.Cleanup(func() {
		helperWriteFile(leaderGate, "exit")
		release()
		child.Stop()
		awaitExit(t, child)
	})
	awaitMarker(t, ready)
	helperWriteFile(leaderGate, "exit")
	select {
	case <-barrier.entered:
	case <-time.After(testDeadline):
		t.Fatal("leader exit was not observed before the membership barrier")
	}
	// Simulate an incompatible external reaper after exit observation. This
	// leader has no descendants, and the barrier sends no post-reap signals.
	state, err := child.cmd.Process.Wait()
	if err != nil || state == nil {
		t.Fatalf("external reaping state=%v err=%v", state, err)
	}
	release()
	exit := awaitExit(t, child)
	if exit.Cleanup != CleanupUnconfirmed || !errors.Is(exit.Err, ErrCleanupUnconfirmed) {
		t.Fatalf("missing reaping evidence was treated as safe: %+v", exit)
	}
}
