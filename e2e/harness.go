//go:build e2e || e2e_docker

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

// BuildFixture cross-builds a static fixture in the existing module.
func BuildFixture(t *testing.T, root, goos, arch string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if goos == "windows" {
		path += ".exe"
	}
	ctx, cancel := context.WithTimeout(t.Context(), testsupport.Scale(2*time.Minute))
	defer cancel()
	// #nosec G204 -- Builds the fixed fixture package into a test-owned temporary directory.
	cmd := exec.CommandContext(ctx, "go", "build", "-tags", "e2e", "-o", path, "./e2e/fixture")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return path
}

// Decode requires every stdout line to be one protocol object.
func Decode(t *testing.T, data []byte) []wire.Record {
	t.Helper()
	records, err := wire.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// Assert checks observed records against the explicit platform contract.
func Assert(t *testing.T, s Scenario, class Class, goos string, records []wire.Record) {
	t.Helper()
	want, ok := s.Expectations[class]
	if !ok {
		t.Fatalf("missing %s expectation", class)
	}
	find := func(event string) wire.Record {
		for _, r := range records {
			if r.Event == event {
				return r
			}
		}
		t.Fatalf("missing %s event in %+v", event, records)
		return wire.Record{}
	}
	if want.Capability != "" {
		assertCapability(t, s, class, goos, want, find)
		return
	}
	switch s.ID {
	case "supervisor-death", "watch-parent":
		assertCrash(t, s, want, find)
	case "start-failure":
		assertStartFailures(t, records, find)
	case "repeated-launches":
		assertResources(t, records, find)
	case "immediate-natural-exit":
		assertImmediateNaturalExit(t, want, find)
	default:
		assertTree(t, s, want, records, find)
	}
}

func assertAlive(t *testing.T, r wire.Record, want int) {
	t.Helper()
	count := 0
	if len(r.PIDs) == 0 || len(r.PIDs) != len(r.Alive) {
		t.Fatalf("invalid observer: %+v", r)
	}
	for _, alive := range r.Alive {
		if alive {
			count++
		}
	}
	if count != want {
		t.Fatalf("observer=%+v, want %d live processes", r, want)
	}
}

// CheckCoverage rejects undocumented scenarios and guarantee rows without scenarios.
func CheckCoverage(t *testing.T, root string) {
	t.Helper()
	// #nosec G304 -- Root is the local checkout or explicitly configured CI checkout.
	data, err := os.ReadFile(filepath.Join(root, "docs/guides/platform-guarantees.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		id := strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`")
		if rows[id] {
			t.Fatalf("duplicate guarantee ID %s", id)
		}
		rows[id] = true
	}
	if len(rows) == 0 {
		t.Fatal("no guarantee rows found")
	}
	seen := map[string]bool{}
	for _, s := range Scenarios {
		if !rows[s.ID] {
			t.Errorf("scenario %s has no guarantee row", s.ID)
		}
		seen[s.ID] = true
		for _, class := range []Class{Linux, Darwin, BSD, Windows} {
			if _, ok := s.Expectations[class]; !ok {
				t.Errorf("%s lacks %s expectation", s.ID, class)
			}
		}
	}
	for id := range rows {
		if !seen[id] {
			t.Errorf("guarantee %s has no scenario", id)
		}
	}
}

// RunNative bounds the subprocess and preserves stderr separately from JSON.
func RunNative(t *testing.T, fixture string, args ...string) []wire.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wire.Deadline+testsupport.Scale(5*time.Second))
	defer cancel()
	// #nosec G204 -- Fixture is built locally or explicitly supplied by the CI harness.
	cmd := exec.CommandContext(ctx, fixture, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture %v: %v\nstdout: %s\nstderr: %s", args, err, stdout.String(), stderr.String())
	}
	return Decode(t, stdout.Bytes())
}

// ScenarioByID resolves Docker-only variations to the shared contract.
func ScenarioByID(id string) Scenario {
	for _, s := range Scenarios {
		if s.ID == id {
			return s
		}
	}
	panic("unknown scenario " + id)
}

func assertLimits(t *testing.T, mode string, r *wire.LimitObservation) {
	t.Helper()
	if r == nil {
		t.Fatal("missing observed kernel limits")
	}
	if mode == "parent" || mode == "address-space" {
		want := min(uint64(wire.AddressSpace), r.HardAddressSpace)
		if want == 0 || r.BeforeAddressSpace == want || r.AddressSpace != want {
			t.Fatalf("address-space limit not changed to %d: %+v", want, r)
		}
	}
	if mode == "parent" || mode == "oom" {
		if r.WantedOOM <= r.BeforeOOM || r.OOM != r.WantedOOM {
			t.Fatalf("OOM score not increased: %+v", r)
		}
	}
}

func assertEscaped(t *testing.T, records []wire.Record, leader int, want int, observed wire.Record) {
	t.Helper()
	identities := map[int]wire.Record{}
	for _, r := range records {
		if r.Event != "identity" {
			continue
		}
		if _, exists := identities[r.PID]; exists {
			t.Fatalf("duplicate identity: %+v", r)
		}
		identities[r.PID] = r
	}
	original, ok := identities[leader]
	if !ok || original.PGID != leader || original.SID <= 0 || len(identities) != want+1 {
		t.Fatalf("invalid leader identity: %+v", identities)
	}
	escaped := 0
	for i, pid := range observed.PIDs {
		if pid == leader {
			continue
		}
		r, ok := identities[pid]
		if !ok || !observed.Alive[i] || r.PGID != pid || r.SID != pid || r.PGID == original.PGID || r.SID == original.SID {
			t.Fatalf("descendant did not escape into a live separate session: %+v", r)
		}
		escaped++
	}
	if escaped != want {
		t.Fatalf("escaped descendants=%d, want %d", escaped, want)
	}
}

func assertCapability(t *testing.T, s Scenario, class Class, goos string, want Expectation, find func(string) wire.Record) {
	t.Helper()
	r := find("capability")
	capability := want.Capability
	capability = assertCapabilityOutcome(t, capability, goos, r)
	if capability == "ok" && want.Limits != "" {
		assertLimits(t, want.Limits, r.Limits)
	}
	if capability == "ok" && s.ID == "extra-files" && (r.Data != wire.ExtraAck || r.Exit == nil) {
		t.Fatalf("missing descriptor 3 read acknowledgement: %+v", r)
	}
	if capability == "ok" && s.ID == "stdin" && (r.Data != wire.StdinPayload || r.Exit == nil) {
		t.Fatalf("missing standard input echo: %+v", r)
	}
	if capability == "ok" && s.ID == "cgroup-v2" {
		if err := assertCgroupOutcome(r, find); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := assertParentLimitsOutcome(s, class, r, find); err != nil {
		t.Fatal(err)
	}
	if r.Exit != nil && (r.Exit.Err != "" || !r.Exit.CleanupConfirmed || (s.ID == "extra-files" && r.Exit.Code != 0)) {
		t.Fatalf("capability exit lacks clean confirmed completion: %+v", r.Exit)
	}
}

// assertParentLimitsOutcome carries the parent-limits specific checks of the
// capability record. They apply to every capability: on non-Linux platforms
// the expectation is "unsupported", and the record must still prove that the
// launch was refused with exactly one AfterStart call reporting pid 0.
func assertParentLimitsOutcome(s Scenario, class Class, r wire.Record, find func(string) wire.Record) error {
	if s.ID != "parent-limits" {
		return nil
	}
	if r.Calls != 1 {
		return fmt.Errorf("AfterStart calls=%d", r.Calls)
	}
	if class != Linux && r.PID != 0 {
		return errors.New("unsupported limit launched child")
	}
	if class == Linux {
		return assertAliveErr(find("observed"), 0)
	}
	return nil
}

// assertAliveErr is the error-returning form of assertAlive.
func assertAliveErr(r wire.Record, want int) error {
	if len(r.PIDs) == 0 || len(r.PIDs) != len(r.Alive) {
		return fmt.Errorf("invalid observer: %+v", r)
	}
	count := 0
	for _, alive := range r.Alive {
		if alive {
			count++
		}
	}
	if count != want {
		return fmt.Errorf("observer=%+v, want %d live processes", r, want)
	}
	return nil
}

// assertCgroupOutcome requires the scenario terminal record, not just a
// successful start: clean exit code, no exit error, no terminal record
// error, an observed leaf path and its removal are each part of the
// contract. A missing terminal record is itself a failure, so a terminal
// cgroup failure cannot hide behind an unread second capability event.
// Leaf removal is confirmed by fs.ErrNotExist only; any other stat error is
// an inconclusive observation reported through the shared e2eprotocol rule.
func assertCgroupOutcome(capability wire.Record, find func(string) wire.Record) error {
	exited := find("exited")
	if exited.Exit == nil {
		return fmt.Errorf("cgroup scenario has no terminal exit record (capability=%+v, exited=%+v)", capability, exited)
	}
	if exited.Exit.Code != 0 || exited.Exit.Err != "" || !exited.Exit.CleanupConfirmed {
		return fmt.Errorf("cgroup child did not exit with confirmed cleanup: code=%d cleanup_confirmed=%t err=%q data=%q", exited.Exit.Code, exited.Exit.CleanupConfirmed, exited.Exit.Err, exited.Data)
	}
	if exited.Error != "" {
		return fmt.Errorf("cgroup scenario reported a terminal error: %s (data=%q)", exited.Error, exited.Data)
	}
	if exited.Data == "" {
		return errors.New("cgroup scenario did not report the launch leaf path")
	}
	if err := wire.LeafRemoved(exited.Data); err != nil {
		return fmt.Errorf("cgroup leaf cleanup: %w", err)
	}
	return nil
}

func assertCrash(t *testing.T, s Scenario, want Expectation, find func(string) wire.Record) {
	t.Helper()
	killed := find("supervisor-killed")
	assertAlive(t, find("observed"), want.Survivors)
	if s.ID == "supervisor-death" {
		markers := find("markers")
		if markers.Markers != want.Markers || len(markers.PIDs) != want.Markers {
			t.Fatalf("parent-death SIGTERM markers=%+v, want %d", markers, want.Markers)
		}
		if want.Markers > 0 && markers.PIDs[0] != find("started").PID {
			t.Fatal("SIGTERM marker was not from the leader")
		}
	}
	if want.ParentCause {
		cause := find("parent-exit")
		if cause.CauseType != "*procguard.ParentExitError" || cause.ExpectedPID != killed.PID || cause.ObservedPID <= 0 || cause.ObservedPID == cause.ExpectedPID {
			t.Fatalf("parent cause: expected *procguard.ParentExitError with ExpectedPID=%d and a positive, different ObservedPID; observed %+v", killed.PID, cause)
		}
		if want.Subreaper && (cause.ObservedPID != killed.ObservedPID || cause.ObservedPID == 1) {
			t.Fatalf("subreaper adoption: expected ObservedPID=%d (intermediate anchor, not PID 1); observed %+v", killed.ObservedPID, cause)
		}
	}
}

func assertStartFailures(t *testing.T, records []wire.Record, find func(string) wire.Record) {
	t.Helper()
	calls, failures, delays := 0, 0, 0
	for _, r := range records {
		switch r.Event {
		case "after-start":
			calls++
			if r.Error == "" || r.PID != 0 || r.Calls != calls {
				t.Fatalf("failed-start callback: %+v", r)
			}
		case "start-failed":
			failures++
		case "backoff":
			delays++
			if r.Delay <= 0 {
				t.Fatal("backoff did not progress")
			}
		}
	}
	if calls != 3 || failures != 3 || delays != 3 {
		t.Fatalf("attempts=%d failures=%d delays=%d", calls, failures, delays)
	}
	state := find("state").State
	if state == nil || state.Running || state.Launches != 0 || state.Failures != 3 || state.DroppedEvents != 0 {
		t.Fatalf("failure state: %+v", state)
	}
	outcome := find("outcome").Outcome
	if outcome == nil || outcome.Reason != procguard.Stopped {
		t.Fatalf("Stop outcome: %+v", outcome)
	}
}

func assertResources(t *testing.T, records []wire.Record, find func(string) wire.Record) {
	t.Helper()
	r := find("resources")
	if r.BeforeFD == 0 || r.BeforeThreads == 0 || r.AfterFD > r.BeforeFD+4 || r.AfterThreads > r.BeforeThreads+8 {
		t.Fatalf("resource growth: %+v", r)
	}
	state := find("state").State
	if state == nil || state.Launches != 50 || state.Restarts != 49 || state.DroppedEvents != 0 {
		t.Fatalf("restart state: %+v", state)
	}
	for _, r := range records {
		if r.Exit != nil && r.Exit.Err != "" {
			t.Fatalf("restart exit: %+v", r.Exit)
		}
	}
}

func assertTree(t *testing.T, s Scenario, want Expectation, records []wire.Record, find func(string) wire.Record) {
	t.Helper()
	record := find("exited")
	if want.MaxWait > 0 && (record.Delay < 0 || record.Delay >= testsupport.Scale(want.MaxWait)) {
		t.Fatalf("%s stop delay=%v, want less than %v", s.ID, record.Delay, testsupport.Scale(want.MaxWait))
	}
	exit := record.Exit
	if exit == nil || exit.Err != "" || !exit.CleanupConfirmed || exit.Killed != want.Killed {
		t.Fatalf("exit=%+v, want Killed=%v", exit, want.Killed)
	}
	if exit.PID <= 0 || exit.StartedAt.IsZero() || exit.ExitedAt.Before(exit.StartedAt) {
		t.Fatalf("incomplete Exit: %+v", exit)
	}
	if s.ID != "natural-exit" && s.ID != "empty-unit" && !exit.Requested {
		t.Fatal("stop was not recorded")
	}
	assertTreeMarkers(t, s, want, exit.PID, find)
	if want.Escaped > 0 {
		assertEscaped(t, records, exit.PID, want.Escaped, find("observed"))
	}
	assertAlive(t, find("observed"), want.Survivors)
	find("ready")
	find("started")
	outcome := find("outcome").Outcome
	if outcome == nil || !outcome.CleanupConfirmed || outcome.Err != "" {
		t.Fatalf("outcome lacks confirmed cleanup: %+v", outcome)
	}
	if s.ID == "immediate-cancel" && outcome.Reason != procguard.Cancelled {
		t.Fatalf("cancellation outcome=%+v, want %v", outcome, procguard.Cancelled)
	}
	find("state")
}

func assertCapabilityOutcome(t *testing.T, capability, goos string, r wire.Record) string {
	t.Helper()
	if capability == "bsd-self-limit" {
		capability = "ok"
		if goos == "openbsd" {
			capability = "unsupported"
		}
	}
	switch capability {
	case "ok":
		if r.Error != "" || r.Unsupported {
			t.Fatalf("capability failed: %+v", r)
		}
	case "unsupported":
		if !r.Unsupported {
			t.Fatalf("expected ErrUnsupported: %+v", r)
		}
	case "error":
		if r.Error == "" || r.Unsupported {
			t.Fatal("expected platform limit error")
		}
	}
	return capability
}

func assertImmediateNaturalExit(t *testing.T, want Expectation, find func(string) wire.Record) {
	t.Helper()
	exit := find("exited").Exit
	if exit == nil || exit.Err != "" || !exit.CleanupConfirmed || exit.Killed != want.Killed || exit.Requested || exit.PID <= 0 || exit.StartedAt.IsZero() || exit.ExitedAt.Before(exit.StartedAt) {
		t.Fatalf("natural exit=%+v, want Killed=%v and no requested stop", exit, want.Killed)
	}
	if record := find("exited"); want.MaxWait > 0 && (record.Delay < 0 || record.Delay >= testsupport.Scale(want.MaxWait)) {
		t.Fatalf("natural-exit cleanup delay=%v, want less than %v", record.Delay, testsupport.Scale(want.MaxWait))
	}
	markers := find("markers")
	if markers.Markers != want.Markers || len(markers.PIDs) != want.Markers {
		t.Fatalf("natural-exit markers=%+v, want %d", markers, want.Markers)
	}
	ready, observed := find("ready"), find("observed")
	if len(ready.PIDs) != 3 || !slices.Equal(ready.PIDs, observed.PIDs) {
		t.Fatalf("incomplete natural-exit tree observation: ready=%+v observed=%+v", ready, observed)
	}
	assertAlive(t, observed, want.Survivors)
	outcome := find("outcome").Outcome
	if outcome == nil || !outcome.CleanupConfirmed || outcome.Err != "" {
		t.Fatalf("natural-exit outcome lacks confirmed cleanup: %+v", outcome)
	}
	find("outcome")
	find("state")
}

func assertTreeMarkers(t *testing.T, s Scenario, want Expectation, leader int, find func(string) wire.Record) {
	t.Helper()
	markers := find("markers")
	if markers.Markers != want.Markers || len(markers.PIDs) != want.Markers {
		t.Fatalf("markers=%+v, want %d", markers, want.Markers)
	}
	ready := find("ready")
	count := 3
	if s.ID == "empty-unit" {
		count = 1
	}
	if len(ready.PIDs) != count || !slices.Equal(ready.PIDs, find("observed").PIDs) {
		t.Fatalf("incomplete tree observation: %+v", ready)
	}
	for _, pid := range markers.PIDs {
		if !slices.Contains(ready.PIDs, pid) || (s.ID == "natural-exit" && pid == leader) {
			t.Fatalf("unexpected SIGTERM marker PID %d", pid)
		}
	}
}
