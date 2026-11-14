//go:build e2e

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"github.com/ovineko/procguard/internal/testsupport"
)

// startGatedGrandchild starts the test binary itself in grandchild mode with
// the cgroup scenario's exit gate but no cgroup request, so the parent-side
// flow — readiness barrier, membership observation, gated exit, cleanup — is
// testable on every platform without a delegated cgroup subtree.
func startGatedGrandchild(t *testing.T, dir string) *procguard.Child {
	t.Helper()
	child, err := procguard.Start(context.Background(), procguard.Spec{
		Path:        os.Args[0],
		Args:        []string{"grandchild", "-scenario", "instant", "-dir", dir, "-gate", filepath.Join(dir, cgroupExitGate)},
		GracePeriod: testsupport.Scale(700 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("start gated grandchild: %v", err)
	}
	c := config{scenario: "cgroup-v2", dir: dir}
	t.Cleanup(func() { cgroupCleanup(child, c) })
	return child
}

// TestCgroupOutcomeHoldsChildUntilGateReleased drives the parent schedule
// through barriers instead of sleeps: the membership observation blocks on a
// channel while the supervised child must stay owned and unexited, proving
// the child cannot exit until the parent permits it.
func TestCgroupOutcomeHoldsChildUntilGateReleased(t *testing.T) {
	dir := t.TempDir()
	child := startGatedGrandchild(t, dir)
	c := config{scenario: "cgroup-v2", dir: dir}

	membershipEntered := make(chan struct{})
	releaseMembership := make(chan struct{})
	observedLeaf := filepath.Join(dir, "observed-leaf") // absent: confirmed removal
	original := readCgroupLeaf
	readCgroupLeaf = func(int) (string, error) {
		close(membershipEntered)
		<-releaseMembership
		return observedLeaf, nil
	}
	defer func() { readCgroupLeaf = original }()

	type outcome struct {
		record wire.Record
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		record, err := cgroupOutcome(child, c)
		done <- outcome{record, err}
	}()

	select {
	case <-membershipEntered:
	case <-time.After(wire.Deadline):
		t.Fatal("membership observation never started")
	}

	select {
	case <-child.Done():
		t.Fatal("cgroup child exited before the exit gate was released")
	default:
	}
	close(releaseMembership)

	var out outcome
	select {
	case out = <-done:
	case <-time.After(wire.Deadline):
		t.Fatal("gated outcome did not finish after the gate release")
	}
	if out.err != nil {
		t.Fatalf("gated outcome failed: %v", out.err)
	}
	if out.record.Data != observedLeaf {
		t.Fatalf("exited record lost the observed leaf: %+v", out.record)
	}
	if out.record.Exit == nil || out.record.Exit.Code != 0 || out.record.Exit.Err != "" {
		t.Fatalf("child did not exit cleanly after the gate release: %+v", out.record.Exit)
	}
	if !exists(filepath.Join(dir, cgroupExitGate)) {
		t.Fatal("exit gate was never released")
	}
}

// TestCgroupOutcomeMembershipReadErrorFailsAndCleansUp proves an injected
// membership read error fails the scenario with its cause, and that cleanup
// releases the gate and reaps the supervised unit instead of leaking it.
func TestCgroupOutcomeMembershipReadErrorFailsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	child := startGatedGrandchild(t, dir)
	c := config{scenario: "cgroup-v2", dir: dir}

	injected := errors.New("injected membership failure")
	original := readCgroupLeaf
	readCgroupLeaf = func(int) (string, error) { return "", injected }
	defer func() { readCgroupLeaf = original }()

	record, err := cgroupOutcome(child, c)
	if !errors.Is(err, injected) {
		t.Fatalf("membership read error must fail the scenario with its cause, got record=%+v err=%v", record, err)
	}
	if record.Event != "" {
		t.Fatalf("a mid-flow failure must not emit an exited record, got %+v", record)
	}
	select {
	case <-child.Done():
	case <-time.After(wire.Deadline):
		t.Fatal("supervised child leaked after the membership failure")
	}
	if !exists(filepath.Join(dir, cgroupExitGate)) {
		t.Fatal("cleanup did not release the exit gate")
	}
}

// TestCgroupOutcomeRejectsPresentLeaf proves the fixture-side terminal
// record rejects a leaf that is still observable, with the child reaped by
// cleanup on that failure path.
func TestCgroupOutcomeRejectsPresentLeaf(t *testing.T) {
	dir := t.TempDir()
	child := startGatedGrandchild(t, dir)
	c := config{scenario: "cgroup-v2", dir: dir}

	presentLeaf := filepath.Join(dir, "present-leaf")
	if err := os.Mkdir(presentLeaf, 0o700); err != nil {
		t.Fatal(err)
	}
	original := readCgroupLeaf
	readCgroupLeaf = func(int) (string, error) { return presentLeaf, nil }
	defer func() { readCgroupLeaf = original }()

	record, err := cgroupOutcome(child, c)
	if err == nil {
		t.Fatal("a present launch leaf must fail the scenario")
	}
	if record.Event != "exited" || record.Data != presentLeaf || record.Error == "" {
		t.Fatalf("terminal record must carry the present leaf and its error: %+v", record)
	}
	select {
	case <-child.Done():
	case <-time.After(wire.Deadline):
		t.Fatal("supervised child leaked after the leaf rejection")
	}
}
