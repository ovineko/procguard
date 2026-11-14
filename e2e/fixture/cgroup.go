//go:build e2e

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

// cgroupExitGate is the exit-gate marker the supervised child waits on; the
// parent creates it only after the membership observation is complete.
const cgroupExitGate = "cgroup-exit-gate"

// readCgroupLeaf reports the launch leaf directory of pid from its live
// cgroup v2 membership. The seam lets fixture tests inject membership read
// failures without a delegated cgroup subtree.
var readCgroupLeaf = func(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", fmt.Errorf("read /proc/%d/cgroup: %w", pid, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "0::"); ok && after != "" {
			return "/sys/fs/cgroup" + after, nil
		}
	}
	return "", errors.New("no cgroup v2 membership record")
}

// cgroupCapability exercises one cgroup v2 launch end to end. The capability
// record carries only the start outcome — mechanism availability, nothing
// more. The scenario outcome (clean exit, membership, leaf removal) is
// reported in the mandatory "exited" record: the harness fails the scenario
// when that record is missing or reports a bad final state, so a terminal
// failure can never hide behind a second, unread capability event.
// PROCGUARD_E2E_CGROUP_PARENT overrides the delegated parent; without it the
// fixture derives its own cgroup.
func cgroupCapability(c config) error {
	cfg := &procguard.CgroupConfig{
		Parent:    wire.CgroupParent(),
		MemoryMax: 16 << 20,
	}
	var calls, pid int
	var startErr error
	spec := procguard.Spec{
		Path:        os.Args[0],
		Args:        []string{"grandchild", "-scenario", "instant", "-dir", c.dir, "-gate", filepath.Join(c.dir, cgroupExitGate)},
		GracePeriod: c.grace,
		Cgroup:      cfg,
		AfterStart: func(after int, err error) {
			calls++
			pid = after
			startErr = err
		},
	}
	child, err := procguard.Start(context.Background(), spec)
	if err != nil {
		// A failed Start is still a complete capability observation: the
		// scenario outcome record below is not expected in this branch.
		emit(wire.Record{
			Event:       "capability",
			Calls:       calls,
			PID:         pid,
			Unsupported: errors.Is(err, procguard.ErrUnsupported),
			Permission:  cgroupDelegationDenied(err),
			Error:       err.Error(),
		})
		return nil
	}
	record := wire.Record{Event: "capability", Calls: calls, PID: pid}
	if startErr != nil {
		// AfterStart reported a problem even though Start succeeded; the
		// exit record carries the scenario outcome.
		record.Error = startErr.Error()
	}
	emit(record)

	out, flowErr := cgroupOutcome(child, c)
	if out.Event != "" {
		emit(out)
	}
	return flowErr
}

// cgroupOutcome owns the scenario after a successful start: the child holds
// on cgroupExitGate while the parent observes readiness and membership, so
// the membership read never races the child's exit. Cleanup releases the
// gate and stops the unit on every return path.
func cgroupOutcome(child *procguard.Child, c config) (wire.Record, error) {
	defer func() { cgroupCleanup(child, c) }()

	ctx, cancel := bounded(context.Background())
	defer cancel()

	if err := until(ctx, func() bool { return exists(filepath.Join(c.dir, strconv.Itoa(child.PID())+".ready")) }); err != nil {
		return wire.Record{}, fmt.Errorf("cgroup child readiness: %w", err)
	}
	leafDir, err := readCgroupLeaf(child.PID())
	if err != nil {
		return wire.Record{}, fmt.Errorf("cgroup membership: %w", err)
	}
	if err := mark(c.dir, cgroupExitGate, "exit"); err != nil {
		return wire.Record{}, fmt.Errorf("cgroup exit gate: %w", err)
	}
	select {
	case <-child.Done():
	case <-ctx.Done():
		return wire.Record{Data: leafDir}, fmt.Errorf("cgroup child exit: %w", ctx.Err())
	}
	exit := child.Wait()

	out := wire.Record{Event: "exited", Exit: wire.Snapshot(exit), Data: leafDir}
	var flowErr error
	switch {
	case exit.Err != nil:
		flowErr = fmt.Errorf("cgroup child exit code=%d: %w", exit.Code, exit.Err)
		out.Error = fmt.Sprintf("cgroup child exit: code=%d err=%v", exit.Code, exit.Err)
	case exit.Code != 0:
		flowErr = fmt.Errorf("cgroup child exit: code=%d", exit.Code)
		out.Error = flowErr.Error()
	default:
		if leafErr := wire.LeafRemoved(leafDir); leafErr != nil {
			flowErr = leafErr
			out.Error = leafErr.Error()
		}
	}
	return out, flowErr
}

// cgroupCleanup releases the exit gate before escalating because the
// grandchild's gate branch ignores signals until the gate appears, and it
// bounds every wait on the shared fixture deadline.
func cgroupCleanup(child *procguard.Child, c config) {
	ctx, cancel := bounded(context.Background())
	defer cancel()
	_ = mark(c.dir, cgroupExitGate, "exit")
	child.Stop()
	select {
	case <-child.Done():
		child.Wait()
	case <-ctx.Done():
	}
}
