package procguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRestartPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		policy     RestartPolicy
	}{
		{"never", "17", RestartPolicy{Mode: RestartNever}},
		{"success", "0", RestartPolicy{Mode: RestartOnFailure}},
		{"prevent", "42", RestartPolicy{PreventExitCodes: []int{42}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return helperSpec("exit", tc.code), nil }, Restart: tc.policy})
			result := awaitOutcome(t, results)
			if result.Reason != Completed || result.Last == nil || s.State().Launches != 1 || s.State().Restarts != 0 {
				t.Fatalf("outcome/state: %+v %+v", result, s.State())
			}
			if tc.name == "prevent" && result.Last.Code != 42 {
				t.Fatal(result.Last.Code)
			}
			result.Last.Code = 99
			if s.State().Last.Code == 99 {
				t.Fatal("outcome aliases state")
			}
		})
	}
}

func TestCrashBackoff(t *testing.T) {
	for _, mode := range []RestartMode{RestartAlways, RestartOnFailure} {
		t.Run(map[RestartMode]string{RestartAlways: "always", RestartOnFailure: "failure"}[mode], func(t *testing.T) {
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return helperSpec("exit", "3"), nil }, Restart: RestartPolicy{Mode: mode}, Backoff: shortBackoff()})
			for _, want := range []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond} {
				e := awaitEvent(t, s, EventRestartScheduled)
				if e.Delay != want {
					t.Fatalf("delay %v want %v", e.Delay, want)
				}
			}
			s.Stop()
			result := awaitOutcome(t, results)
			if result.Reason != Stopped || s.State().Failures < 5 {
				t.Fatalf("result/state: %+v %+v", result, s.State())
			}
		})
	}
}

func TestExplicitRestart(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "immediate"}[immediate], func(t *testing.T) {
			var out observedOutput
			b := shortBackoff()
			b.Next()
			b.Next()
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { spec := helperSpec("wait"); spec.Stdout = &out; return spec, nil }, Backoff: b, Restart: RestartPolicy{Mode: RestartNever}})
			first := awaitEvent(t, s, EventStarted)
			awaitReady(t, &out)
			s.Restart(immediate)
			scheduled := awaitEvent(t, s, EventRestartScheduled)
			want := 20 * time.Millisecond
			if immediate {
				want = 0
			}
			if scheduled.Delay != want {
				t.Fatalf("delay %v want %v", scheduled.Delay, want)
			}
			second := awaitEvent(t, s, EventStarted)
			if second.PID == first.PID || s.State().Restarts != 1 {
				t.Fatalf("restart: %+v", s.State())
			}
			s.Stop()
			if result := awaitOutcome(t, results); result.Reason != Stopped {
				t.Fatal(result)
			}
			launches := s.State().Launches
			s.Restart(true)
			if s.State().Launches != launches || s.State().Running {
				t.Fatal("restarted after stop")
			}
		})
	}
}

func TestStableReset(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "runtime", true: "mark"}[marked], func(t *testing.T) {
			var out observedOutput
			b := shortBackoff()
			b.Next()
			b.Next()
			if !marked {
				b.stableAfter = time.Nanosecond
			}
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { spec := helperSpec("wait"); spec.Stdout = &out; return spec, nil }, Backoff: b})
			awaitEvent(t, s, EventStarted)
			awaitReady(t, &out)
			if marked {
				s.MarkStable()
			}
			s.Restart(false)
			if e := awaitEvent(t, s, EventRestartScheduled); e.Delay != 5*time.Millisecond {
				t.Fatalf("delay: %v", e.Delay)
			}
			s.Stop()
			awaitOutcome(t, results)
		})
	}
}

func TestSupervisorCancellation(t *testing.T) {
	for _, phase := range []string{"running", "backoff", "before"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			b := NewBackoff(time.Hour, time.Hour)
			s, results := runSupervisor(ctx, t, Options{Spec: func(context.Context) (Spec, error) {
				if phase == "backoff" {
					return helperSpec("exit", "1"), nil
				}
				return helperSpec("wait"), nil
			}, Backoff: b})
			if phase == "running" {
				awaitEvent(t, s, EventStarted)
			}
			if phase == "backoff" {
				awaitEvent(t, s, EventRestartScheduled)
			}
			cancel()
			result := awaitOutcome(t, results)
			if result.Reason != Cancelled || s.State().Running {
				t.Fatalf("outcome/state: %+v %+v", result, s.State())
			}
		})
	}
}

func TestSpawnFailuresAndWake(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "immediate"}[immediate], func(t *testing.T) {
			b := NewBackoff(time.Hour, time.Hour)
			b.base = time.Hour
			calls := 0
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) {
				calls++
				if calls == 1 {
					return Spec{Path: filepath.Join(t.TempDir(), "missing")}, nil
				}
				return helperSpec("wait"), nil
			}, Backoff: b})
			awaitEvent(t, s, EventStartFailed)
			awaitEvent(t, s, EventRestartScheduled)
			if state := s.State(); state.Failures != 1 || state.Restarts != 0 || state.Launches != 0 {
				t.Fatalf("state: %+v", state)
			}
			s.Restart(immediate)
			awaitEvent(t, s, EventStarted)
			if immediate && s.State().Failures != 0 {
				t.Fatal("immediate did not reset")
			}
			s.Stop()
			awaitOutcome(t, results)
		})
	}
}

func TestStopSpawnRaces(t *testing.T) {
	for _, phase := range []string{"before", "window", "factory", "after-start"} {
		t.Run(phase, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			block := func() { close(entered); <-release }
			s, err := NewSupervisor(Options{Spec: func(context.Context) (Spec, error) {
				if phase == "factory" {
					block()
				}
				spec := helperSpec("ignore")
				if phase == "after-start" {
					spec.AfterStart = func(_ int, _ error) { block() }
				}
				return spec, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			if phase == "window" {
				s.testHookSpawnWindow = block
			}
			if phase == "before" {
				s.Stop()
			}
			results := make(chan Outcome, 1)
			go func() { results <- s.Run(t.Context()) }()
			if phase != "before" {
				select {
				case <-entered:
				case <-time.After(testDeadline):
					t.Fatal("spawn window not reached")
				}
				s.Stop()
				close(release)
			}
			result := awaitOutcome(t, results)
			if result.Reason != Stopped || s.State().Running {
				t.Fatalf("outcome/state: %+v %+v", result, s.State())
			}
			if phase == "after-start" && (result.Last == nil || !result.Last.Requested) {
				t.Fatal("spawn escaped stop")
			}
			if phase == "before" && result.Last != nil {
				t.Fatal("launched after stop")
			}
		})
	}
}

func TestStopRestartStorm(t *testing.T) {
	for range 5 {
		s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return helperSpec("wait"), nil }, Backoff: shortBackoff(), EventBuffer: 1})
		eventually(t, func() bool { return s.State().Running })
		var wg sync.WaitGroup
		for n := range 20 {
			wg.Go(func() {
				for range 10 {
					s.Restart(n%2 == 0)
					s.MarkStable()
					s.State()
					_ = s.Signal(os.Interrupt)
				}
				s.Stop()
			})
		}
		wg.Wait()
		result := awaitOutcome(t, results)
		if result.Reason != Stopped || s.State().Running || s.State().DroppedEvents == 0 {
			t.Fatalf("state: %+v", s.State())
		}
	}
}

func TestSupervisorValidation(t *testing.T) {
	for _, opts := range []Options{{}, {Spec: func(context.Context) (Spec, error) { return Spec{}, nil }, EventBuffer: -1}, {Spec: func(context.Context) (Spec, error) { return Spec{}, nil }, Restart: RestartPolicy{Mode: 99}}} {
		if _, err := NewSupervisor(opts); err == nil {
			t.Error("invalid options accepted")
		}
	}
}

func TestSpecFailure(t *testing.T) {
	s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return Spec{}, errors.New("factory failed") }, Backoff: shortBackoff()})
	if event := awaitEvent(t, s, EventStartFailed); event.Err == nil {
		t.Fatal("missing factory error")
	}
	awaitEvent(t, s, EventRestartScheduled)
	s.Stop()
	awaitOutcome(t, results)
}

func TestStabilityDuringAfterStart(t *testing.T) {
	b := shortBackoff()
	b.Next()
	b.Next()
	var s *Supervisor
	opts := Options{Backoff: b, Spec: func(context.Context) (Spec, error) {
		spec := helperSpec("exit", "1")
		spec.AfterStart = func(_ int, err error) {
			if err == nil {
				s.MarkStable()
			}
		}
		return spec, nil
	}}
	var err error
	s, err = NewSupervisor(opts)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan Outcome, 1)
	go func() { results <- s.Run(t.Context()) }()
	t.Cleanup(func() { s.Stop() })
	if event := awaitEvent(t, s, EventRestartScheduled); event.Delay != 5*time.Millisecond {
		t.Fatalf("early readiness lost: %v", event.Delay)
	}
	s.Stop()
	awaitOutcome(t, results)
}

func TestIndependentSupervisors(t *testing.T) {
	first, firstResult := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return helperSpec("wait"), nil }})
	second, secondResult := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) { return helperSpec("wait"), nil }})
	awaitEvent(t, first, EventStarted)
	awaitEvent(t, second, EventStarted)
	first.Stop()
	awaitOutcome(t, firstResult)
	if !second.State().Running || first.State().PID == second.State().PID {
		t.Fatal("supervisors interfere")
	}
	second.Stop()
	awaitOutcome(t, secondResult)
}

func TestRunOnce(t *testing.T) {
	s, err := NewSupervisor(Options{Spec: func(context.Context) (Spec, error) { return helperSpec("exit", "0"), nil }, Restart: RestartPolicy{Mode: RestartNever}})
	if err != nil {
		t.Fatal(err)
	}
	s.Run(t.Context())
	defer func() {
		if recover() == nil {
			t.Error("second Run must panic")
		}
	}()
	s.Run(t.Context())
}

func TestPendingRestartExitPolicy(t *testing.T) {
	for _, prevent := range []bool{false, true} {
		t.Run(map[bool]string{false: "success-overrides-mode", true: "prevent-wins"}[prevent], func(t *testing.T) {
			ready, gate := readyPath(t), readyPath(t)
			policy := RestartPolicy{Mode: RestartOnFailure}
			if prevent {
				policy.PreventExitCodes = []int{0}
			}
			s, results := runSupervisor(t.Context(), t, Options{Spec: func(context.Context) (Spec, error) {
				spec := helperSpec("gated-exit", ready, gate, "0")
				spec.GracePeriod = testScale(5 * time.Second)
				return spec, nil
			}, Restart: policy, Backoff: shortBackoff()})
			awaitEvent(t, s, EventStarted)
			eventually(t, func() bool { _, err := os.Stat(ready); return err == nil })
			s.Restart(true)
			if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
				t.Fatal(err)
			}
			if prevent {
				result := awaitOutcome(t, results)
				if result.Reason != Completed || result.Last == nil || result.Last.Code != 0 || s.State().Restarts != 0 || s.State().Launches != 1 {
					t.Fatalf("outcome/state: %+v %+v", result, s.State())
				}
			} else {
				awaitEvent(t, s, EventStarted)
				s.Stop()
				awaitOutcome(t, results)
			}
		})
	}
}

func TestStateObservesClosedLeaderExit(t *testing.T) {
	exited := make(chan struct{})
	s := &Supervisor{opts: Options{Backoff: NewBackoff(0, 0)}, child: &Child{exited: exited}, state: State{Running: true, PID: 123}}
	close(exited)
	if state := s.State(); state.Running || state.PID != 123 {
		t.Fatalf("state after exit: %+v", state)
	}
}
