//go:build e2e_docker

package docker

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"github.com/ovineko/procguard"
	"github.com/ovineko/procguard/e2e"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

type profile struct {
	name, image, arch string
	flags             []string
	class             e2e.Class
	smoke             bool
}

func profiles() []profile {
	return []profile{
		{"debian", "debian:bookworm-slim", runtime.GOARCH, nil, e2e.Linux, false},
		{"alpine", "alpine:3.21", runtime.GOARCH, nil, e2e.Linux, false},
		{"nonroot", "debian:bookworm-slim", runtime.GOARCH, []string{"--user", "65534:65534"}, e2e.Linux, false},
		{"restricted", "debian:bookworm-slim", runtime.GOARCH, []string{"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only"}, e2e.Linux, false},
		{"pids", "debian:bookworm-slim", runtime.GOARCH, []string{"--pids-limit", "64", "--env", "GOMAXPROCS=2"}, e2e.Linux, false},
		{"pid1-noinit", "debian:bookworm-slim", runtime.GOARCH, nil, e2e.Linux, false},
		{"pid1-init", "debian:bookworm-slim", runtime.GOARCH, []string{"--init"}, e2e.Linux, false},
		{"qemu-arm64", "alpine:3.21", "arm64", nil, e2e.Linux, true},
		{"qemu-riscv64", "debian:trixie-slim", "riscv64", nil, e2e.Linux, true},
	}
}

func TestProfiles(t *testing.T) {
	requireDocker(t)
	e2e.CheckCoverage(t, "../..")
	binaries := map[string]string{}
	for _, p := range profiles() {
		if p.smoke && !wire.QEMU() {
			continue
		}
		if binaries[p.arch] == "" {
			binaries[p.arch] = e2e.BuildFixture(t, "../..", "linux", p.arch)
		}
	}
	for _, p := range profiles() {
		t.Run(p.name, func(t *testing.T) {
			if p.smoke && !wire.QEMU() {
				t.Skip("qemu smoke only: set PROCGUARD_E2E_QEMU=1 after configuring host binfmt")
			}
			binary := binaries[p.arch]
			for _, s := range e2e.Scenarios {
				t.Run(s.ID, func(t *testing.T) {
					if s.ID == "pid1-reaping" {
						if p.name != "pid1-noinit" && p.name != "pid1-init" {
							t.Skip("PID 1 contract is exercised in the two PID 1 profiles")
						}
						records := run(t, p, binary, "supervise", "-scenario", s.ID, "-dir", "/tmp")
						assertReaping(t, p, records)
						return
					}
					if p.smoke && s.ID != "graceful-stop" && s.ID != "tree-termination" && s.ID != "natural-exit" {
						t.Skip("qemu-user smoke subset; not platform certification")
					}
					if s.ID == "cgroup-v2" {
						assertCgroupRefused(t, run(t, p, binary, s.Mode, "-scenario", s.ID, "-dir", "/tmp"))
						return
					}
					var records []wire.Record
					if s.Mode == "anchor" {
						records = crash(t, p, binary, s.ID)
					} else {
						records = run(t, p, binary, s.Mode, "-scenario", s.ID, "-dir", "/tmp")
					}
					e2e.Assert(t, s, p.class, "linux", records)
				})
			}
			if p.name == "pid1-noinit" || p.name == "pid1-init" {
				t.Run("docker-stop", func(t *testing.T) { stopContainer(t, p, binary) })
			}
			if p.name == "nonroot" {
				t.Run("oom-decrease", func(t *testing.T) {
					records := run(t, p, binary, "supervise", "-scenario", "oom-decrease", "-dir", "/tmp")
					assertOOMFailure(t, records)
				})
			}
			if p.name == "pids" {
				t.Run("exhaustion", func(t *testing.T) {
					records := run(t, p, binary, "supervise", "-scenario", "pids-exhaustion", "-dir", "/tmp")
					e2e.Assert(t, e2e.ScenarioByID("start-failure"), p.class, "linux", records)
					assertExhaustion(t, records)
				})
			}
		})
	}
	t.Run("hidepid", func(t *testing.T) {
		t.Skip("a private hidepid proc mount requires privileges; the harness never grants them")
	})
}

func assertCgroupRefused(t *testing.T, records []wire.Record) {
	t.Helper()
	r := one(t, records, "capability")
	if r.PID != 0 || r.Calls != 1 || !r.Permission || r.Error == "" || r.Unsupported {
		t.Fatalf("undelegated cgroup must fail before child creation: %+v", r)
	}
	for _, record := range records {
		if record.Event == "exited" || record.Event == "started" || record.Exit != nil {
			t.Fatalf("undelegated cgroup launched a process: %+v", record)
		}
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testsupport.Scale(10*time.Second))
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput()
	if err != nil {
		if wire.RequireDocker() {
			t.Fatalf("Docker availability: expected docker info success; observed error=%v output=%s", err, out)
		}
		t.Skipf("Docker unavailable: %v: %s; set PROCGUARD_E2E_REQUIRE=1 to fail", err, out)
	}
}

func docker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testsupport.Scale(60*time.Second))
	defer cancel()
	var stdout, stderr bytes.Buffer
	// #nosec G204 -- Arguments come from fixed profiles and locally built fixture paths.
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker %v: expected success; observed error=%v\nstdout=%s\nstderr=%s", args, err, stdout.Bytes(), stderr.Bytes())
	}
	return stdout.Bytes()
}

func launch(t *testing.T, p profile, binary string, args ...string) string {
	t.Helper()
	flags := make([]string, 0, 16+len(p.flags)+len(args))
	flags = append(flags, "run", "--detach", "--platform", "linux/"+p.arch, "--network", "none", "--mount", "type=bind,src="+binary+",dst=/fixture,readonly", "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777")
	flags = append(flags, "--env", "PROCGUARD_TEST_TIME_SCALE="+strconv.Itoa(int(testsupport.Scale(time.Second)/time.Second)))
	flags = append(flags, p.flags...)
	flags = append(flags, p.image, "/fixture")
	flags = append(flags, args...)
	id := strings.TrimSpace(string(docker(t, flags...)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testsupport.Scale(10*time.Second))
		defer cancel()
		// #nosec G204 -- Container ID is returned by docker run.
		if out, err := exec.CommandContext(ctx, "docker", "rm", "--force", id).CombinedOutput(); err != nil {
			t.Errorf("remove container %s: expected success; observed error=%v output=%s", id, err, out)
		}
	})
	return id
}

func collect(t *testing.T, id string) []wire.Record {
	t.Helper()
	code := strings.TrimSpace(string(docker(t, "wait", id)))
	logs := docker(t, "logs", id)
	if code != "0" {
		t.Fatalf("container exit: expected 0; observed %s\n%s", code, logs)
	}
	return e2e.Decode(t, logs)
}

func run(t *testing.T, p profile, binary string, args ...string) []wire.Record {
	t.Helper()
	return collect(t, launch(t, p, binary, args...))
}

func ready(t *testing.T, id string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wire.Deadline)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastOutput []byte
	var lastErr error
	for {
		// #nosec G204 -- Container ID is returned by docker run; command and marker are fixed.
		cmd := exec.CommandContext(ctx, "docker", "exec", id, "cat", "/tmp/supervisor.ready")
		lastOutput, lastErr = cmd.CombinedOutput()
		if lastErr == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(lastOutput)))
			if err != nil || pid <= 0 {
				t.Fatalf("supervisor readiness PID: expected positive integer; observed output=%q pid=%d error=%v", lastOutput, pid, err)
			}
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("supervisor readiness: expected readable /tmp/supervisor.ready within %s; observed deadline=%v last error=%v output=%q\n%s", wire.Deadline, ctx.Err(), lastErr, lastOutput, docker(t, "logs", id))
		case <-ticker.C:
		}
	}
}

func crash(t *testing.T, p profile, binary, id string) []wire.Record {
	t.Helper()
	container := launch(t, p, binary, "anchor", "-scenario", id, "-dir", "/tmp", "-gate", "external")
	pid := ready(t, container)
	docker(t, "exec", container, "/bin/sh", "-c", `kill -9 "$1"`, "kill", strconv.Itoa(pid))
	return collect(t, container)
}

func stopContainer(t *testing.T, p profile, binary string) {
	t.Helper()
	id := launch(t, p, binary, "supervise", "-scenario", "docker-stop", "-dir", "/tmp")
	pid := ready(t, id)
	wantPID1 := p.name == "pid1-noinit"
	if wantPID1 != (pid == 1) {
		t.Fatalf("%s supervisor: expected PID==1 to be %t; observed PID=%d", p.name, wantPID1, pid)
	}
	started := time.Now()
	docker(t, "stop", "--time", strconv.Itoa(int(testsupport.Scale(5*time.Second)/time.Second)), id)
	if elapsed := time.Since(started); elapsed >= testsupport.Scale(5*time.Second) {
		t.Fatalf("docker stop duration: expected <5s; observed %s", elapsed)
	}
	records := collect(t, id)
	e2e.Assert(t, e2e.ScenarioByID("graceful-stop"), p.class, "linux", records)
	outcome := one(t, records, "outcome").Outcome
	exit := one(t, records, "exited").Exit
	if outcome == nil || outcome.Reason != procguard.Cancelled || outcome.Last == nil {
		t.Fatalf("docker SIGTERM outcome: expected non-nil outcome with Reason=Cancelled (%d), Last non-nil; observed %+v", procguard.Cancelled, outcome)
	}
	if exit == nil || !exit.Requested || exit.Killed || exit.Code != 0 {
		t.Fatalf("docker SIGTERM exit: expected non-nil Exit with Requested=true Killed=false Code=0; observed %+v", exit)
	}
}

func assertReaping(t *testing.T, p profile, records []wire.Record) {
	t.Helper()
	state := one(t, records, "state").State
	outcome := one(t, records, "outcome").Outcome
	// Each approved restart calls Backoff.Next. PreventExitCodes stops the third
	// launch before restartDelay, retaining the two failures already recorded.
	if state == nil || state.Running || state.Launches != 3 || state.Restarts != 2 || state.Failures != 2 || state.DroppedEvents != 0 {
		t.Fatalf("restart state: expected non-nil State with Running=false Launches=3 Restarts=2 Failures=2 DroppedEvents=0; observed %+v", state)
	}
	if state.Last == nil || state.Last.Code != 42 {
		t.Fatalf("restart state last exit: expected non-nil Last with Code=42; observed %+v", state.Last)
	}
	if outcome == nil || outcome.Reason != procguard.Completed {
		t.Fatalf("restart outcome: expected non-nil Outcome with Reason=Completed (%d); observed %+v", procguard.Completed, outcome)
	}
	if outcome.Last == nil || outcome.Last.Code != 42 {
		t.Fatalf("restart outcome last exit: expected non-nil Last with Code=42; observed %+v", outcome.Last)
	}
	samples, exits := 0, 0
	for _, r := range records {
		switch r.Event {
		case "zombie-sample":
			samples++
			if p.name == "pid1-noinit" && r.Zombies != samples*2 {
				t.Fatalf("zombie sample %d: expected Zombies=%d; observed %+v", samples, samples*2, r)
			}
		case "exited":
			exits++
			wantCode := 1
			if exits == 3 {
				wantCode = 42
			}
			if r.Exit == nil || r.Exit.Code != wantCode || r.Exit.Err != "" || r.Exit.Requested {
				t.Fatalf("crash exit %d: expected non-nil Exit with Code=%d Err=\"\" Requested=false; observed %+v", exits, wantCode, r.Exit)
			}
		}
	}
	if samples != 3 || exits != 3 {
		t.Fatalf("restart events: expected samples=3 exits=3; observed samples=%d exits=%d", samples, exits)
	}
	observed := one(t, records, "observed")
	assertDead(t, observed, 9)
	want := 0
	if p.name == "pid1-noinit" {
		want = 6
	}
	if observed.Zombies != want {
		t.Fatalf("%s final zombies: expected %d; observed %d", p.name, want, observed.Zombies)
	}
}

func assertOOMFailure(t *testing.T, records []wire.Record) {
	t.Helper()
	result := one(t, records, "capability")
	callback := one(t, records, "after-start")
	if result.Error == "" || !result.Permission || result.Unsupported || result.PID <= 0 || result.Calls != 1 || result.Exit != nil {
		t.Fatalf("OOM decrease: expected Error nonempty Permission=true Unsupported=false PID>0 Calls=1 Exit=nil; observed %+v", result)
	}
	if callback.Error != result.Error || callback.Calls != 1 || callback.PID != result.PID {
		t.Fatalf("OOM callback: expected Error=%q Calls=1 PID=%d; observed %+v", result.Error, result.PID, callback)
	}
	observed := one(t, records, "observed")
	assertDead(t, observed, 1)
	if observed.PIDs[0] != result.PID || observed.Zombies != 0 {
		t.Fatalf("failed child cleanup: expected PIDs=[%d] Zombies=0; observed %+v", result.PID, observed)
	}
}

func assertExhaustion(t *testing.T, records []wire.Record) {
	t.Helper()
	failures, callbacks, delays := 0, 0, 0
	var previous time.Duration
	for _, r := range records {
		switch r.Event {
		case "after-start":
			callbacks++
			if !r.ResourceExhausted {
				t.Fatalf("exhaustion callback %d: expected ResourceExhausted=true (EAGAIN); observed %+v", callbacks, r)
			}
		case "start-failed":
			failures++
			if !r.ResourceExhausted {
				t.Fatalf("exhaustion launch %d: expected ResourceExhausted=true (EAGAIN); observed %+v", failures, r)
			}
		case "backoff":
			delays++
			if r.Delay <= previous {
				t.Fatalf("exhaustion backoff %d: expected delay >%s; observed %s", delays, previous, r.Delay)
			}
			previous = r.Delay
		}
	}
	if failures != 3 || callbacks != 3 || delays != 3 {
		t.Fatalf("exhaustion events: expected failures=3 callbacks=3 delays=3; observed failures=%d callbacks=%d delays=%d", failures, callbacks, delays)
	}
	observed := one(t, records, "observed")
	if len(observed.PIDs) == 0 {
		t.Fatalf("cgroup fillers: expected at least one PID; observed %+v", observed)
	}
	assertDead(t, observed, len(observed.PIDs))
	if observed.Zombies != 0 {
		t.Fatalf("filler cleanup: expected Zombies=0; observed %+v", observed)
	}
}

func one(t *testing.T, records []wire.Record, event string) wire.Record {
	t.Helper()
	var result wire.Record
	count := 0
	for _, r := range records {
		if r.Event == event {
			result = r
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%s events: expected count=1; observed count=%d records=%+v", event, count, records)
	}
	return result
}

func assertDead(t *testing.T, r wire.Record, count int) {
	t.Helper()
	if len(r.PIDs) != count || len(r.Alive) != count {
		t.Fatalf("observer: expected %d PIDs and %d Alive entries; observed %+v", count, count, r)
	}
	seen := map[int]bool{}
	for i, pid := range r.PIDs {
		if pid <= 0 || seen[pid] || r.Alive[i] {
			t.Fatalf("observer entry %d: expected PID>0 duplicate=false Alive=false; observed PID=%d duplicate=%t Alive=%t record=%+v", i, pid, seen[pid], r.Alive[i], r)
		}
		seen[pid] = true
	}
}
