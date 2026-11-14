package procguard

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
)

var modelSeed = flag.Int64("seed", 1, "Supervisor model schedule seed")

type supervisorModel struct {
	mode                         RestartMode
	launches, restarts, failures int
	stable                       bool
	reason                       OutcomeReason
}

func (m *supervisorModel) transition(action string, code int) bool {
	switch action {
	case "stop":
		m.reason = Stopped
		return false
	case "cancel":
		m.reason = Cancelled
		return false
	}
	explicit := action == "restart" || action == "immediate"
	relaunch := code != 42 && (explicit || m.mode == RestartAlways || (m.mode == RestartOnFailure && code != 0))
	if !relaunch {
		m.reason = Completed
		return false
	}
	m.restarts++
	if m.stable || action == "immediate" {
		m.failures = 0
	}
	if action != "immediate" {
		m.failures++
	}
	m.stable = false
	m.launches++
	return true
}

func TestSupervisorModel(t *testing.T) {
	runs := testsupport.ModelRuns()
	t.Logf("seed=%d schedules=%d", *modelSeed, runs)
	for i := range runs {
		seed := *modelSeed + int64(i)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { runModelSchedule(t, seed) })
	}
}

func runModelSchedule(t *testing.T, seed int64) {
	t.Helper()
	t.Logf("reproduce: go test -race -run '^TestSupervisorModel$/^seed=%d$' -seed=%d", seed, seed)
	before := testsupport.Stacks()
	t.Cleanup(func() {
		if err := testsupport.WaitNoLeaks(before); err != nil {
			t.Error(err)
		}
	})
	random := rand.New(rand.NewSource(seed))
	model := supervisorModel{mode: RestartMode(random.Intn(3)), launches: 1}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	launch := 0
	ignore := random.Intn(2) == 0
	mode := "graceful"
	if ignore {
		mode = "ignore"
	}
	s, results := runSupervisor(ctx, t, Options{
		Restart: RestartPolicy{Mode: model.mode, PreventExitCodes: []int{42}},
		Backoff: shortBackoff(), EventBuffer: 128,
		Spec: func(context.Context) (Spec, error) {
			launch++
			spec := helperSpec("model", mode, filepath.Join(dir, strconv.Itoa(launch)))
			spec.GracePeriod = testScale(5 * time.Millisecond)
			return spec, nil
		},
	})
	var pids []int
	var previous time.Time
	next := func(kind EventKind) Event {
		t.Helper()
		select {
		case event, ok := <-s.Events():
			if !ok || event.Kind != kind || event.Time.Before(previous) {
				t.Fatalf("event order: got %+v open=%t want %v", event, ok, kind)
			}
			previous = event.Time
			return event
		case <-time.After(testDeadline):
			t.Fatalf("event %v timed out", kind)
		}
		return Event{}
	}
	started := next(EventStarted)
	steps := 2 + random.Intn(7)
	for step := 0; ; step++ {
		pids = append(pids, started.PID)
		path := filepath.Join(dir, strconv.Itoa(model.launches))
		eventually(t, func() bool { _, err := os.Stat(path + ".ready"); return err == nil })
		if state := s.State(); !state.Running || state.Launches != model.launches || state.Restarts != model.restarts || state.Failures != model.failures {
			t.Fatalf("model=%+v state=%+v", model, state)
		}
		s.mu.Lock()
		child := s.child
		s.mu.Unlock()
		if random.Intn(2) == 0 {
			s.MarkStable()
			model.stable = true
		}
		actions := []string{"restart", "immediate", "exit0", "exit7", "signal", "crash", "prevent", "stop", "cancel"}
		action := actions[random.Intn(len(actions))]
		if step == steps {
			action = []string{"stop", "cancel", "prevent"}[random.Intn(3)]
		}
		t.Logf("step=%d action=%s ignore-term=%t", step, action, ignore)
		// Jitter changes scheduling; readiness is established by the marker above.
		<-time.After(testScale(time.Duration(random.Intn(500)) * time.Microsecond))
		code := 0
		switch action {
		case "stop":
			s.Stop()
			s.Restart(random.Intn(2) == 0)
		case "cancel":
			cancel()
		case "restart":
			s.Restart(false)
		case "immediate":
			s.Restart(true)
		case "signal":
			code = -1
			if runtime.GOOS == "windows" {
				code = 1
			}
			if err := s.Signal(os.Kill); err != nil {
				t.Fatal(err)
			}
		default:
			if action == "exit7" {
				code = 7
			}
			if action == "prevent" {
				code = 42
			}
			command := strconv.Itoa(code)
			if action == "crash" {
				command = "crash"
				code = -1
				if runtime.GOOS == "windows" {
					code = 1
				}
			}
			if err := os.WriteFile(path+".exit", []byte(command), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-child.Exited():
		case <-time.After(testDeadline):
			t.Fatal("leader observation timed out")
		}
		state := s.State()
		if state.PID == child.PID() && state.Running {
			t.Fatal("State.Running after observed leader exit")
		}
		exited := next(EventExited)
		if exited.PID != started.PID || exited.Exit == nil || exited.Exit.Err != nil {
			t.Fatalf("exit: %+v", exited)
		}
		if action == "exit0" || action == "exit7" || action == "prevent" || action == "signal" || action == "crash" {
			if exited.Exit.Code != code {
				t.Fatalf("exit code=%d want=%d", exited.Exit.Code, code)
			}
		}
		if !processGone(t, started.PID) {
			t.Fatal("exited event for live PID")
		}
		if !model.transition(action, code) {
			break
		}
		scheduled := next(EventRestartScheduled)
		wantDelay := time.Duration(0)
		if action != "immediate" {
			wantDelay = min(5*time.Millisecond<<(model.failures-1), 40*time.Millisecond)
		}
		if scheduled.Delay != wantDelay {
			t.Fatalf("delay=%v want=%v", scheduled.Delay, wantDelay)
		}
		started = next(EventStarted)
	}
	outcome := awaitOutcome(t, results)
	next(EventStopped)
	if _, ok := <-s.Events(); ok {
		t.Fatal("events remain open after Run")
	}
	state := s.State()
	if outcome.Reason != model.reason || state.Running || state.Launches != model.launches || state.Restarts != model.restarts || state.Failures != model.failures || state.DroppedEvents != 0 {
		t.Fatalf("model=%+v outcome=%+v state=%+v", model, outcome, state)
	}
	s.Stop()
	s.Restart(true)
	for _, pid := range pids {
		if !processGone(t, pid) {
			t.Fatalf("PID %d alive after Run", pid)
		}
	}
}

func helperModel(args []string) int {
	terms := helperSignals(args[0])
	path := args[1]
	helperWriteFile(path+".ready", "ready")
	timer := time.NewTimer(testDeadline)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-terms:
			return 0
		case <-timer.C:
			return 98
		case <-ticker.C:
			// #nosec G304 G703 -- The re-exec harness supplies a path inside t.TempDir.
			data, err := os.ReadFile(path + ".exit")
			if err == nil && len(data) > 0 {
				if string(data) == "crash" {
					process, findErr := os.FindProcess(os.Getpid())
					if findErr != nil {
						return 98
					}
					_ = process.Kill()
					return 98
				}
				code, err := strconv.Atoi(string(data))
				if err == nil {
					return code
				}
			}
		}
	}
}
