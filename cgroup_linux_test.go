//go:build linux

package procguard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
	"golang.org/x/sys/unix"
)

// cgroupTestParent resolves a delegated cgroup v2 directory the tests may
// create leaves in. The environment override wins; otherwise the user
// delegation slice is probed. Without any writable parent the tests skip
// with the exact missing condition — the runtime requirement is a delegated
// subtree, not merely a mounted one.
func cgroupTestParent(t *testing.T) string {
	t.Helper()
	if err := validateCgroupSpec(&CgroupConfig{}); err != nil {
		t.Skipf("cgroup v2 runtime unavailable: %v", err)
	}
	if parent := os.Getenv("PROCGUARD_TEST_CGROUP_PARENT"); parent != "" {
		return cgroupParentDir(parent)
	}
	for _, parent := range cgroupCandidates() {
		if cgroupParentWritable(parent) {
			return parent
		}
	}
	t.Skipf("no delegated cgroup v2 parent available (tried %v); set PROCGUARD_TEST_CGROUP_PARENT to run these tests", cgroupCandidates())
	return ""
}

func cgroupCandidates() []string {
	var candidates []string
	if rel, err := selfCgroupRelative(); err == nil {
		candidates = append(candidates, filepath.Join(cgroupRoot, rel))
	}
	return append(candidates,
		filepath.Join(cgroupRoot, fmt.Sprintf("user.slice/user-%d.slice/user@%d.service", os.Getuid(), os.Getuid())),
	)
}

// cgroupMigratableParent resolves a parent that also permits clone-time
// placement with the requested limit. The kernel requires write access to
// cgroup.procs of BOTH the source and the destination cgroup, so a launch
// from a non-delegated scope is denied with EACCES even into a user-owned
// leaf; and a parent that holds the launcher itself cannot enable
// controllers (the no-internal-process rule). Each candidate is probed with
// the real configuration, and tests that need placement skip with the exact
// remedy when no candidate qualifies.
func cgroupMigratableParent(t *testing.T, memoryMax int64) string {
	t.Helper()
	if parent := os.Getenv("PROCGUARD_TEST_CGROUP_PARENT"); parent != "" {
		dir := cgroupParentDir(parent)
		// An explicitly configured parent must work: falling back to another
		// candidate would mask a broken test setup behind a green run.
		if err := cgroupPlacementProbe(dir, memoryMax); err != nil {
			t.Fatalf("PROCGUARD_TEST_CGROUP_PARENT=%s does not provide placement (memoryMax=%d): %v", parent, memoryMax, err)
		}
		return dir
	}
	var lastErr error
	for _, parent := range cgroupCandidates() {
		if !cgroupParentWritable(parent) {
			continue
		}
		if err := cgroupPlacementProbe(parent, memoryMax); err != nil {
			lastErr = err
			continue
		}
		return parent
	}
	msg := fmt.Sprintf("no cgroup v2 parent permits clone-time placement with memoryMax=%d (%v); "+
		"run the suite inside a delegated scope: systemd-run --user --scope -p Delegate=yes go test -run TestCgroup", memoryMax, lastErr)
	if os.Getenv("PROCGUARD_TEST_CGROUP_REQUIRE") == "1" {
		t.Fatal(msg)
	}
	t.Skip(msg)
	return ""
}

// cgroupPlacementProbe runs one real placement and observes the child to the
// end: a Start that spawns but cannot finish the probe is a failure, not a
// capability.
func cgroupPlacementProbe(parent string, memoryMax int64) error {
	spec := helperSpec("exit", "0")
	spec.Cgroup = &CgroupConfig{Parent: parent, MemoryMax: memoryMax}
	child, err := Start(context.Background(), spec)
	if err != nil {
		return err
	}
	if exit := child.Wait(); exit.Err != nil {
		return fmt.Errorf("probe child: %w", exit.Err)
	}
	return nil
}

func cgroupParentWritable(parent string) bool {
	probe := filepath.Join(parent, cgroupLeafName())
	if err := os.Mkdir(probe, 0o755); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func countLeaves(parent string) int {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pguard.") {
			count++
		}
	}
	return count
}

func TestCgroupNestedMembersRemainAndEnumerate(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "nested", "worker")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "cgroup.procs"), []byte("202\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cg := &cgroupResources{path: root}
	remaining, err := cg.remaining()
	if err != nil || !remaining {
		t.Fatalf("nested member remaining = %v, %v; want true, nil", remaining, err)
	}
	pids, err := cg.members()
	if err != nil || len(pids) != 1 || pids[0] != 202 {
		t.Fatalf("subtree members = %v, %v; want [202]", pids, err)
	}
}

func fdCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func cgroupChildSpec(t *testing.T, parent string, memoryMax int64, args ...string) (Spec, *observedOutput) {
	t.Helper()
	out := &observedOutput{}
	spec := helperSpec(args...)
	spec.Stdout = out
	spec.Cgroup = &CgroupConfig{Parent: parent, MemoryMax: memoryMax}
	return spec, out
}

func startCgroupChild(t *testing.T, spec Spec) *Child {
	t.Helper()
	child, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("cgroup Start: %v", err)
	}
	return child
}

func grandchildGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func childPIDAlive(pid int) bool {
	return !grandchildGone(pid)
}

// The kernel applies memory.max at clone, so user code starts inside an
// already-limited leaf — and the child can observe its own limit.
func TestCgroupLimitVisibleBeforeUserCode(t *testing.T) {
	parent := cgroupMigratableParent(t, 16<<20)
	spec, out := cgroupChildSpec(t, parent, 16<<20, "cgroup-self-max")
	child := startCgroupChild(t, spec)
	exit := awaitExit(t, child)
	if exit.Err != nil || exit.Code != 0 {
		t.Fatalf("exit: %+v", exit)
	}
	if got := strings.TrimSpace(out.String()); got != strconv.Itoa(16<<20) {
		t.Fatalf("child observed memory.max = %q, want %d", got, 16<<20)
	}
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaf leaked after exit: %d remain", leaves)
	}
}

// The limit is enforced, not merely recorded: exceeding it gets the child
// killed by the kernel, before the helper's hold window expires.
func TestCgroupMemoryMaxEnforced(t *testing.T) {
	parent := cgroupMigratableParent(t, 8<<20)
	spec, _ := cgroupChildSpec(t, parent, 8<<20, "cgroup-alloc", "32", "5000")
	child := startCgroupChild(t, spec)
	exit := awaitExit(t, child)
	if exit.Signal != syscall.SIGKILL {
		t.Fatalf("over-limit child must die by kernel SIGKILL, got %+v", exit)
	}
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaf leaked after enforcement: %d remain", leaves)
	}
}

// Descendants spawned by the child are members of the same leaf, and Stop
// empties the whole unit — the setsid variant is the documented pgroup
// escape that the cgroup closes.
func TestCgroupDescendantsAreContained(t *testing.T) {
	for _, setsid := range []string{"0", "1"} {
		t.Run("setsid="+setsid, func(t *testing.T) {
			parent := cgroupMigratableParent(t, 0)
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "grand-pid")
			ready := filepath.Join(dir, "ready")
			spec, _ := cgroupChildSpec(t, parent, 0, "cgroup-spawn", setsid, "30", pidFile, ready, "stay")
			child := startCgroupChild(t, spec)
			helperAwaitFile(ready)

			raw, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			grand, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			// The grandchild reports the leaf as its own cgroup, even when it
			// called setsid.
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", grand))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "/pguard.") {
				t.Fatalf("grandchild cgroup = %q, want a pguard leaf", data)
			}

			child.Stop()
			exit := awaitExit(t, child)
			if exit.Err != nil {
				t.Fatalf("exit: %+v", exit)
			}
			eventually(t, func() bool { return grandchildGone(grand) })
			if leaves := countLeaves(parent); leaves != 0 {
				t.Fatalf("leaf leaked after stop: %d remain", leaves)
			}
		})
	}
}

func TestCgroupNestedWorkerCleanupConfirmed(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%t", fallback), func(t *testing.T) {
			parent := cgroupMigratableParent(t, 0)
			dir := t.TempDir()
			pidFile, ready, gate := filepath.Join(dir, "worker-pid"), filepath.Join(dir, "ready"), filepath.Join(dir, "exit")
			spec, _ := cgroupChildSpec(t, parent, 0, "cgroup-spawn", "1", "30", pidFile, ready, "stay", "exit-gate", gate)
			spec.GracePeriod = testScale(50 * time.Millisecond)
			child, err := startWith(context.Background(), spec, launchHooks{
				newTree: func(pid int, res *cgroupResources) (*processTree, error) {
					tree, err := newProcessTree(pid, res)
					if err == nil && fallback {
						tree.cg.killFile = false
					}
					return tree, err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				helperWriteFile(gate, "exit")
				_ = child.tree.cg.kill()
				child.Stop()
				awaitExit(t, child)
			})
			helperAwaitFile(ready)
			raw, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			worker, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			leaf := child.tree.cg.path
			nested := filepath.Join(leaf, "nested", "worker")
			if err := os.MkdirAll(nested, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nested, "cgroup.procs"), []byte(strconv.Itoa(worker)), 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", worker))
			if err != nil || !strings.Contains(string(data), strings.TrimPrefix(nested, cgroupRoot)+"\n") {
				t.Fatalf("worker membership=%q err=%v, want %s", data, err, nested)
			}
			helperWriteFile(gate, "exit")
			exit := awaitExit(t, child)
			if exit.Err != nil || exit.Cleanup != CleanupConfirmed || !exit.Killed || exit.Code != 0 {
				t.Fatalf("nested worker natural-exit cleanup: %+v", exit)
			}
			eventually(t, func() bool { return grandchildGone(worker) })
			if _, err := os.Stat(leaf); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("owned subtree remains: %v", err)
			}
		})
	}
}

func TestCgroupMalformedMembershipFailsClosed(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "cgroup.procs"), []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if remaining, err := (&cgroupResources{path: root}).remaining(); err == nil || remaining {
		t.Fatalf("malformed nested membership must fail closed, got remaining=%v err=%v", remaining, err)
	}
}

func TestCgroupPopulatedStateFailsClosed(t *testing.T) {
	for _, value := range []string{"populated 0garbage\n", "populated 0 extra\n", "frozen 0\n"} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "cgroup.events"), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if remaining, err := (&cgroupResources{path: root}).remaining(); err == nil || remaining {
				t.Fatalf("invalid populated state must not confirm cleanup: remaining=%v err=%v", remaining, err)
			}
		})
	}
}

// A leader that exits naturally leaves live descendants: the unit cleanup
// must end them, drain the leaf, and remove it.
func TestCgroupLeaderExitWithDescendants(t *testing.T) {
	parent := cgroupMigratableParent(t, 0)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grand-pid")
	ready := filepath.Join(dir, "ready")
	spec, _ := cgroupChildSpec(t, parent, 0, "cgroup-spawn", "0", "30", pidFile, ready, "exit")
	child := startCgroupChild(t, spec)
	helperAwaitFile(ready)

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	grand, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if exit.Err != nil || exit.Code != 0 {
		t.Fatalf("leader exit: %+v", exit)
	}
	eventually(t, func() bool { return grandchildGone(grand) })
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaf leaked after natural exit: %d remain", leaves)
	}
}

// Unsatisfiable requests fail before any child code runs: Start returns an
// error naming the cause, reports pid 0 through AfterStart, and leaves no
// leaf behind.
func TestCgroupUnsatisfiableRequestFailsBeforeChildCode(t *testing.T) {
	parent := cgroupTestParent(t)
	cases := []struct {
		name    string
		cfg     CgroupConfig
		wantErr string
	}{
		{"missing parent", CgroupConfig{Parent: filepath.Join(parent, "no-such-delegation")}, "delegated parent"},
		{"memory limit on non-cgroup parent", CgroupConfig{Parent: t.TempDir(), MemoryMax: 8 << 20}, "not a cgroup v2 directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sawChild := false
			spec := helperSpec("exit", "0")
			spec.Cgroup = &tc.cfg
			spec.AfterStart = func(pid int, err error) { sawChild = pid != 0 }
			child, err := Start(context.Background(), spec)
			if err == nil {
				child.Stop()
				_ = awaitExit(t, child)
				t.Fatal("expected failure")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if sawChild {
				t.Fatal("child code must never run for an unsatisfiable request")
			}
			if leaves := countLeaves(parent); leaves != 0 {
				t.Fatalf("leaf leaked after failure: %d remain", leaves)
			}
		})
	}
}

// A cancelled start context fails the launch before the leaf is created.
func TestCgroupCancelledStartLeavesNothing(t *testing.T) {
	parent := cgroupTestParent(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spec := helperSpec("exit", "0")
	spec.Cgroup = &CgroupConfig{Parent: parent}
	if child, err := Start(ctx, spec); err == nil {
		child.Stop()
		_ = awaitExit(t, child)
		t.Fatal("cancelled context must fail the start")
	}
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaf leaked after cancelled start: %d remain", leaves)
	}
}

// Repeated launches release their leaves and procguard's own descriptors.
func TestCgroupRepeatedLaunchesDoNotLeak(t *testing.T) {
	parent := cgroupMigratableParent(t, 0)
	before := fdCount()
	for range 5 {
		spec, _ := cgroupChildSpec(t, parent, 0, "exit", "0")
		child := startCgroupChild(t, spec)
		if exit := awaitExit(t, child); exit.Err != nil {
			t.Fatalf("exit: %+v", exit)
		}
	}
	if after := fdCount(); after > before+2 {
		t.Fatalf("descriptor leak: %d -> %d", before, after)
	}
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaves leaked: %d remain", leaves)
	}
}

// killMember must never signal a process outside the named leaf: a PID
// reused between enumeration and delivery is refused here, not killed.
func TestCgroupKillMemberRefusesNonMember(t *testing.T) {
	spec := helperSpec("sleep-sec", "30")
	out := &observedOutput{}
	spec.Stdout = out
	child := startHelper(t, spec)
	defer func() { child.Stop(); _ = child.Wait() }()

	pid := child.PID()
	dead := func() bool { return grandchildGone(pid) }
	if dead() {
		t.Fatal("sleeper must be alive")
	}

	// A non-member PID (wrong leaf) is skipped without a signal.
	if err := killMember(pid, "/nonexistent-leaf"); err != nil {
		t.Fatalf("non-member refusal must not error, got %v", err)
	}
	if dead() {
		t.Fatal("non-member process must survive")
	}

	// The real member is killed through the pinned descriptor.
	memberLeaf := "/nonexistent-leaf"
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid)); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "0::"); ok {
				memberLeaf = after
			}
		}
	}
	if err := killMember(pid, memberLeaf); err != nil {
		t.Fatalf("member kill: %v", err)
	}
	eventually(t, dead)
}

// A refused swap pin is a failed launch — the advertised hard cap must not
// degrade into reclaim absorption — while a host without swap control at all
// proceeds without the pin.
func TestCgroupSwapPinFailureIsFailClosed(t *testing.T) {
	parent := cgroupMigratableParent(t, 8<<20)

	orig := writeSwapMax
	writeSwapMax = func(string, string) error { return fs.ErrPermission }
	cfg := &CgroupConfig{Parent: parent, MemoryMax: 8 << 20}
	_, err := newCgroupResources(cfg)
	writeSwapMax = orig
	if err == nil || !strings.Contains(err.Error(), "memory.swap.max") {
		t.Fatalf("refused swap pin must fail the launch, got %v", err)
	}
	if leaves := countLeaves(parent); leaves != 0 {
		t.Fatalf("leaf leaked after swap pin failure: %d remain", leaves)
	}

	writeSwapMax = func(string, string) error { return fs.ErrNotExist }
	res, err := newCgroupResources(cfg)
	writeSwapMax = orig
	if err != nil {
		t.Fatalf("missing swap control must proceed without the pin: %v", err)
	}
	if err := res.discard(); err != nil {
		t.Fatal(err)
	}
}

// The fallback sweep must not claim success while the leaf still holds
// members: a bounded round cap ends in an explicit error (which routes the
// caller to the process-group kill), and an unrelated process is never
// signalled along the way.
func TestCgroupSweepUndrainedLeafIsExplicitError(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("sleep-sec", "30")
	out := &observedOutput{}
	spec.Stdout = out
	child := startHelper(t, spec)
	defer func() { child.Stop(); _ = child.Wait() }()

	// A fake leaf whose membership names the sleeper — a process that is not
	// actually inside this directory, so the verification skips it and the
	// leaf can never drain.
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(child.PID())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cg := &cgroupResources{path: dir}
	err := cg.kill()
	if err == nil || !strings.Contains(err.Error(), "did not drain") {
		t.Fatalf("undrained leaf must end in an explicit error, got %v", err)
	}
	if !childPIDAlive(child.PID()) {
		t.Fatal("unrelated process must survive the sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.procs")); err != nil {
		t.Fatalf("sweep must not mutate membership files: %v", err)
	}
}

// A membership check that cannot be completed is an error, never a silent
// skip: silently skipping would leave a verified member alive past the kill.
func TestCgroupKillMemberVerificationErrorPropagates(t *testing.T) {
	orig := processInLeafFn
	processInLeafFn = func(int, string) (bool, error) { return false, errors.New("membership unreadable") }
	defer func() { processInLeafFn = orig }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	cg := &cgroupResources{path: dir}
	err := cg.kill()
	if err == nil || !strings.Contains(err.Error(), "verify membership") {
		t.Fatalf("unverifiable membership must fail the kill, got %v", err)
	}
}

// Child-level regression for the failed-escalation path: a leader that
// ignores SIGTERM reaches the hard-kill escalation, the escalation reports
// an error, and a setsid descendant survives it — Wait must still return,
// with the escalation error surfaced, instead of polling the survivor
// forever.
func TestCgroupEscalationFailureEndsWaitWithError(t *testing.T) {
	parent := cgroupMigratableParent(t, 0)
	dir := t.TempDir()
	pidFile, ready := filepath.Join(dir, "grand-pid"), filepath.Join(dir, "ready")

	spec := helperSpec("cgroup-spawn", "1", "30", pidFile, ready, "stay", "ignore-term")
	spec.GracePeriod = testScale(300 * time.Millisecond)
	spec.Cgroup = &CgroupConfig{Parent: parent}
	child, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	helperAwaitFile(ready)

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	grand, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}

	// This kernel has cgroup.kill, which would drain the leaf atomically and
	// never reach the fallback under test: strip the leaf's write permission
	// so the kill-file write fails and the pidfd sweep runs.
	leafRel := "/nonexistent"
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", child.PID())); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "0::"); ok {
				leafRel = after
			}
		}
	}
	_ = leafRel
	// This kernel has cgroup.kill, which would drain the leaf atomically and
	// never reach the fallback under test: clear the cached capability so the
	// launch takes the pidfd sweep, whose failure surfaces the escalation.
	child.tree.cg.killFile = false

	orig := processInLeafFn
	processInLeafFn = func(int, string) (bool, error) { return false, errors.New("membership unreadable") }

	child.Stop()
	started := time.Now()
	exit := awaitExit(t, child)
	processInLeafFn = orig

	if time.Since(started) > testScale(10*time.Second) {
		t.Fatalf("Wait hung past the escalation drain window: %v", time.Since(started))
	}
	if exit.Err == nil || !strings.Contains(exit.Err.Error(), "verify membership") {
		t.Fatalf("escalation error must surface in Exit.Err, got %+v", exit)
	}
	if !exit.Killed {
		t.Error("hard kill was needed and reported")
	}

	// Nothing in the library could reach the setsid survivor; clean it up so
	// the test leaves no processes behind.
	_ = syscall.Kill(grand, syscall.SIGKILL)
	eventually(t, func() bool { return grandchildGone(grand) })
}

// A refused write to an existing cgroup.kill must not silently downgrade to
// the sweep: the cause travels with the result, and the sweep itself still
// runs and still refuses to claim success on an undrained leaf.
func TestCgroupKillWriteRefusalSurfacesThroughSweep(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("sleep-sec", "30")
	out := &observedOutput{}
	spec.Stdout = out
	child := startHelper(t, spec)
	defer func() { child.Stop(); _ = child.Wait() }()

	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(child.PID())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := writeCgroupKill
	writeCgroupKill = func(string) error { return os.ErrPermission }
	cg := &cgroupResources{path: dir, killFile: true}
	err := cg.kill()
	writeCgroupKill = orig

	if err == nil {
		t.Fatal("refused cgroup.kill write must surface an error")
	}
	if !strings.Contains(err.Error(), "write cgroup.kill") {
		t.Fatalf("error must name the refused kill write, got %v", err)
	}
	if !strings.Contains(err.Error(), "did not drain") {
		t.Fatalf("error must report the undrained leaf, got %v", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("cause must survive errors.Is, got %v", err)
	}
	if !childPIDAlive(child.PID()) {
		t.Fatal("unrelated process must survive the sweep")
	}
}

// End-to-end on the real delegated leaf: the atomic cgroup.kill path (this
// kernel has it) drains a leaf whose leader forks a setsid descendant, and
// the launch leaf is removed with the unit. The fallback sweep's
// post-first-snapshot member is covered deterministically by
// TestCgroupSweepSignalsMemberAppearingAfterFirstSnapshot; the undrained
// outcome by TestCgroupSweepUndrainedLeafIsExplicitError.
func TestCgroupSweepCompletesLateDescendant(t *testing.T) {
	parent := cgroupMigratableParent(t, 0)
	dir := t.TempDir()
	pidFile, ready := filepath.Join(dir, "grand-pid"), filepath.Join(dir, "ready")
	lateFile := filepath.Join(dir, "late-pid")

	spec := helperSpec("cgroup-spawn", "1", "30", pidFile, ready, "stay", "ignore-term", lateFile)
	spec.GracePeriod = testScale(300 * time.Millisecond)
	spec.Cgroup = &CgroupConfig{Parent: parent}
	child, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	helperAwaitFile(ready)

	leafRel := "/nonexistent"
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", child.PID())); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "0::"); ok {
				leafRel = after
			}
		}
	}
	leafDir := filepath.Join("/sys/fs/cgroup", leafRel)

	child.Stop()
	exit := awaitExit(t, child)
	if exit.Err != nil {
		t.Fatalf("drained sweep must finish cleanly: %+v", exit)
	}
	if !exit.Killed {
		t.Error("hard kill was needed for the surviving members")
	}

	// The late descendant really appeared (forked on the polite SIGTERM) and
	// the sweep drained it together with the leader and the first grandchild.
	if raw, err := os.ReadFile(lateFile); err != nil || strings.TrimSpace(string(raw)) == "" {
		t.Fatalf("late descendant must have been forked: %v %q", err, raw)
	}
	deadline := time.Now().Add(testScale(5 * time.Second))
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(lateFile)
		if err != nil {
			t.Fatal(err)
		}
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && grandchildGone(pid) {
			break
		}
		time.Sleep(treePollInterval)
	}
	if raw, err := os.ReadFile(lateFile); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && !grandchildGone(pid) {
			t.Fatalf("late descendant %d survived the sweep", pid)
		}
	}

	// Repeated Stop after completion stays a no-op.
	child.Stop()

	if _, statErr := os.Stat(leafDir); !os.IsNotExist(statErr) {
		t.Fatalf("leaf must be removed with the unit, statErr = %v", statErr)
	}
}

// The fallback sweep must signal a member that appears only after the first
// snapshot, and the enumeration-seam scripts exactly that: snapshot 1 holds
// only sleeper A; snapshot 2 adds sleeper B. Both are real processes killed
// through real pidfds — the fallback runs even on a kernel with cgroup.kill
// because killFile is forced off — and the scripted leaf drains, so the kill
// succeeds instead of ending in the undrained error.
func TestCgroupSweepSignalsMemberAppearingAfterFirstSnapshot(t *testing.T) {
	dir := t.TempDir()
	type sleeper struct {
		cmd  *exec.Cmd
		done chan struct{}
	}
	sleepers := make([]*sleeper, 0, 2)
	spawnSleeper := func() int {
		cmd := exec.Command(testsupport.Executable(), "-test.run=^TestHelper$", "--", "sleep-sec", "30")
		cmd.Env = helperEnv()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		s := &sleeper{cmd: cmd, done: make(chan struct{})}
		go func() {
			defer close(s.done)
			_, _ = cmd.Process.Wait()
		}()
		sleepers = append(sleepers, s)
		return cmd.Process.Pid
	}
	defer func() {
		for _, s := range sleepers {
			_ = s.cmd.Process.Kill()
		}
	}()
	a := spawnSleeper()
	b := spawnSleeper()

	var calls int
	orig := readMembers
	readMembers = func(*cgroupResources) ([]int, error) {
		calls++
		switch calls {
		case 1:
			return []int{a}, nil // first snapshot: only the original member
		case 2:
			return []int{a, b}, nil // B has appeared after snapshot 1
		default:
			return nil, nil // both were signalled and left the leaf
		}
	}
	membershipConfirmed := func(pid int, _ string) (bool, error) { return pid == a || pid == b, nil }
	origInLeaf := processInLeafFn
	processInLeafFn = membershipConfirmed
	defer func() {
		readMembers = orig
		processInLeafFn = origInLeaf
	}()

	cg := &cgroupResources{path: dir, killFile: false} // fallback forced on a cgroup.kill kernel
	if err := cg.kill(); err != nil {
		t.Fatalf("drained sweep must succeed: %v", err)
	}

	if calls < 3 {
		t.Fatalf("sweep rounds = %d, want at least 3 (snapshot, injection, drain)", calls)
	}

	// SIGKILL was delivered through real pidfds; reap each sleeper and
	// require the wait to complete — an unreaped zombie would answer
	// kill(pid, 0) and mimic a survivor.
	for i, s := range sleepers {
		select {
		case <-s.done:
		case <-time.After(testScale(5 * time.Second)):
			t.Fatalf("sleeper %d was not killed by the sweep", i+1)
		}
	}
}

func helperCgroupSelfMax() int {
	rel, err := selfCgroupRelative()
	if err != nil {
		return 98
	}
	data, err := os.ReadFile(filepath.Join(cgroupRoot, rel, "memory.max"))
	if err != nil {
		return 98
	}
	_, _ = os.Stdout.Write(data)
	return 0
}

func helperCgroupAlloc(args []string) int {
	mb, err := strconv.Atoi(args[0])
	if err != nil {
		return 98
	}
	holdMs, err := strconv.Atoi(args[1])
	if err != nil {
		return 98
	}
	buf := make([]byte, mb*1024*1024)
	for i := range buf {
		buf[i] = byte(i)
	}
	if len(buf) == 0 || buf[len(buf)-1] == 254 && buf[0] == 0 {
		// Keep the touch loop honest without a magic-value trap.
		_ = buf[len(buf)/2]
	}
	<-time.After(testScale(time.Duration(holdMs) * time.Millisecond))
	return 0
}

func helperCgroupSpawn(args []string) int {
	// args: setsid(0/1) sleepSec pidFile readyFile stayOrExit [ignore-term]
	setsid := args[0] == "1"
	grand := exec.Command(testsupport.Executable(), "-test.run=^TestHelper$", "--", "sleep-sec", args[1])
	grand.Env = helperEnv()
	if setsid {
		grand.SysProcAttr = &unix.SysProcAttr{Setsid: true}
	}
	if err := grand.Start(); err != nil {
		return 98
	}
	helperWriteFile(args[2], strconv.Itoa(grand.Process.Pid))
	helperWriteFile(args[3], "ready")
	if len(args) > 6 && args[5] == "exit-gate" {
		helperAwaitFile(args[6])
		return 0
	}
	if args[4] == "exit" {
		return 0
	}
	if len(args) > 5 && args[5] == "ignore-term" {
		// The leader must survive the polite phase so the launch reaches the
		// hard-kill escalation under test. On the polite SIGTERM it forks a
		// new leaf member — the late descendant the sweep has to catch.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		if len(args) > 6 {
			go func() {
				<-sigCh
				late := exec.Command(testsupport.Executable(), "-test.run=^TestHelper$", "--", "sleep-sec", "30")
				late.Env = helperEnv()
				late.SysProcAttr = &unix.SysProcAttr{Setsid: true}
				if late.Start() == nil {
					helperWriteFile(args[6], strconv.Itoa(late.Process.Pid))
				}
			}()
		}
		<-time.After(testScale(30 * time.Second))
		return 0
	}
	<-helperSignals("graceful")
	return 0
}
