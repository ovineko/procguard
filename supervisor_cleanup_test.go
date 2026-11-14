package procguard

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupervisorDoesNotRestartUnconfirmedCleanup(t *testing.T) {
	type testCase struct {
		name    string
		fault   string
		control string
		mode    RestartMode
	}
	cases := []testCase{
		{name: "automatic RestartAlways", fault: "probe", control: "none", mode: RestartAlways},
		{name: "kill error", fault: "kill", control: "none", mode: RestartAlways},
		{name: "RestartNever explicit Restart(false)", fault: "probe", control: "restart-false", mode: RestartNever},
		{name: "RestartNever explicit Restart(true)", fault: "kill", control: "restart-true", mode: RestartNever},
		{name: "Stop race", fault: "probe", control: "stop", mode: RestartAlways},
		{name: "cancel race", fault: "kill", control: "cancel", mode: RestartAlways},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := &observedOutput{}
			spec, memberReady, memberDone, leaderGate := cleanupTreeSpec(t, output)
			releaseProbe := make(chan struct{})
			probeEntered := make(chan struct{})
			var entered atomic.Bool
			fault := &observedFaultTree{
				probeErr: func() error {
					if tc.fault == "probe" {
						return errInjectedCleanupProbe
					}
					return nil
				}(),
				killErr: errInjectedCleanupKill,
			}
			hooks := cleanupTreeHooks(func(underlying cleanupTree) cleanupTree {
				fault.cleanupTree = underlying
				return &barrierFaultTree{observedFaultTree: fault, entered: probeEntered, release: releaseProbe, once: &entered}
			})
			var specCalls atomic.Int32
			opts := Options{
				Spec: func(context.Context) (Spec, error) {
					specCalls.Add(1)
					return spec, nil
				},
				Restart: RestartPolicy{Mode: tc.mode},
				Backoff: shortBackoff(),
			}
			supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
				return startWith(ctx, launch, hooks)
			})
			if err != nil {
				t.Fatalf("construct supervisor: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan Outcome, 1)
			go func() { result <- supervisor.Run(ctx) }()

			awaitMarker(t, memberReady)
			awaitReady(t, output)
			pid := cleanupDescendantPID(t, output)
			releaseOnce := sync.Once{}
			releaseGate := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
			defer func() {
				helperWriteFile(leaderGate, "exit")
				releaseGate()
				if releaseCleanupDescendant(t, pid) {
					awaitMarker(t, memberDone)
				}
			}()
			helperWriteFile(leaderGate, "exit")
			select {
			case <-probeEntered:
			case <-time.After(timeoutForCleanupTest):
				t.Fatal("cleanup did not reach the real membership-probe barrier")
			}
			if !fault.sawLiveMember() {
				t.Fatal("real membership probe did not observe the descendant alive at the barrier")
			}
			switch tc.control {
			case "restart-false":
				supervisor.Restart(false)
			case "restart-true":
				supervisor.Restart(true)
			case "stop":
				supervisor.Stop()
			case "cancel":
				cancel()
			}
			releaseGate()

			var outcome Outcome
			select {
			case outcome = <-result:
			case <-time.After(timeoutForCleanupTest):
				t.Fatal("Supervisor.Run did not finish after failed cleanup observation")
			}
			if outcome.Reason != CleanupFailed || outcome.Cleanup != CleanupUnconfirmed {
				t.Errorf("outcome = %+v, want CleanupFailed with unconfirmed cleanup", outcome)
			}
			if !errors.Is(outcome.Err, ErrCleanupUnconfirmed) {
				t.Errorf("outcome error %v does not retain ErrCleanupUnconfirmed", outcome.Err)
			}
			if tc.fault == "probe" && !errors.Is(outcome.Err, errInjectedCleanupProbe) {
				t.Errorf("outcome error %v lost probe failure", outcome.Err)
			}
			if !errors.Is(outcome.Err, errInjectedCleanupKill) {
				t.Errorf("outcome error %v lost kill failure", outcome.Err)
			}
			if specCalls.Load() != 1 {
				t.Errorf("Spec called %d times, want one generation", specCalls.Load())
			}
			if runtime.GOOS == "windows" {
				eventually(t, func() bool { return processGone(t, pid) })
			} else if processGone(t, pid) {
				t.Error("fault descendant must remain live at the unconfirmed terminal outcome")
			}
		})
	}
}

type barrierFaultTree struct {
	*observedFaultTree

	entered chan struct{}
	release chan struct{}
	once    *atomic.Bool
}

func (t *barrierFaultTree) remaining() (bool, error) {
	remaining, err := t.cleanupTree.remaining()
	t.mu.Lock()
	t.probes = append(t.probes, remaining)
	t.mu.Unlock()
	if remaining && t.once.CompareAndSwap(false, true) {
		close(t.entered)
		<-t.release
	}
	if t.probeErr != nil {
		return remaining, errors.Join(err, t.probeErr)
	}
	return remaining, err
}

func TestSupervisorAlwaysMayRestartConfirmedExitWithWriterError(t *testing.T) {
	var attempts atomic.Int32
	spec := helperSpec("echo")
	spec.Stdout = failingWriter{}
	opts := Options{
		Spec: func(context.Context) (Spec, error) {
			if attempts.Add(1) == 1 {
				return spec, nil
			}
			return helperSpec("exit", "0"), nil
		},
		Restart: RestartPolicy{Mode: RestartAlways},
		Backoff: shortBackoff(),
	}
	supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
		return startWith(ctx, launch, launchHooks{})
	})
	if err != nil {
		t.Fatalf("construct supervisor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan Outcome, 1)
	go func() { result <- supervisor.Run(ctx) }()
	eventually(t, func() bool { return supervisor.State().Launches >= 2 })
	cancel()
	select {
	case <-result:
	case <-time.After(timeoutForCleanupTest):
		t.Fatal("Supervisor.Run did not finish after cancellation")
	}
	confirmedWriterFailure := false
	for event := range supervisor.Events() {
		if event.Kind == EventExited && event.Exit != nil && event.Exit.Code == 0 && event.Exit.Cleanup == CleanupConfirmed && errors.Is(event.Exit.Err, errInjectedOutputWrite) {
			confirmedWriterFailure = true
		}
	}
	if !confirmedWriterFailure {
		t.Fatal("RestartAlways did not pass a confirmed exit with an I/O writer error")
	}
}

var errInjectedOutputWrite = errors.New("injected output writer failure")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errInjectedOutputWrite }

func TestSupervisorRetriesOnlySafeOrConfirmedStartFailures(t *testing.T) {
	t.Run("pre-spawn PID zero is confirmed", func(t *testing.T) {
		var attempts atomic.Int32
		startFailure := make(chan error, 1)
		opts := Options{
			Spec: func(context.Context) (Spec, error) {
				if attempts.Add(1) == 1 {
					spec := helperSpec("exit", "0")
					spec.Path = filepath.Join(t.TempDir(), "missing-executable")
					spec.AfterStart = func(pid int, err error) {
						if pid != 0 {
							startFailure <- errors.New("pre-spawn failure reported nonzero PID")
							return
						}
						startFailure <- err
					}
					return spec, nil
				}
				return helperSpec("sleep-sec", "30"), nil
			},
			Restart: RestartPolicy{Mode: RestartAlways},
			Backoff: shortBackoff(),
		}
		supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
			return startWith(ctx, launch, launchHooks{})
		})
		if err != nil {
			t.Fatalf("construct supervisor: %v", err)
		}
		result := make(chan Outcome, 1)
		go func() { result <- supervisor.Run(context.Background()) }()
		eventually(t, func() bool { return supervisor.State().Launches == 1 })
		supervisor.Stop()
		select {
		case <-result:
		case <-time.After(timeoutForCleanupTest):
			t.Fatal("safe pre-spawn failure did not retry and stop")
		}
		startErr := <-startFailure
		var typed *StartError
		if !errors.As(startErr, &typed) || typed.PID != 0 || typed.Cleanup != CleanupConfirmed {
			t.Errorf("pre-spawn error = %#v, want PID-zero confirmed StartError", startErr)
		}
	})

	t.Run("real post-spawn cleanup confirmed can retry", func(t *testing.T) {
		var attempts atomic.Int32
		callback := make(chan struct {
			pid int
			err error
		}, 1)
		opts := Options{
			Spec: func(context.Context) (Spec, error) {
				n := attempts.Add(1)
				spec := helperSpec("sleep-sec", "30")
				if n == 1 {
					spec.AfterStart = func(pid int, err error) {
						callback <- struct {
							pid int
							err error
						}{pid, err}
					}
				}
				return spec, nil
			},
			Restart: RestartPolicy{Mode: RestartAlways},
			Backoff: shortBackoff(),
		}
		first := atomic.Bool{}
		supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
			if first.CompareAndSwap(false, true) {
				return startWith(ctx, launch, launchHooks{beforeResume: func(*processTree) error { return errInjectedCleanupProbe }})
			}
			return startWith(ctx, launch, launchHooks{})
		})
		if err != nil {
			t.Fatalf("construct supervisor: %v", err)
		}
		result := make(chan Outcome, 1)
		go func() { result <- supervisor.Run(context.Background()) }()
		callbackResult := <-callback
		if callbackResult.pid <= 0 {
			t.Errorf("post-spawn callback PID = %d, want real PID", callbackResult.pid)
		}
		var startErr *StartError
		if !errors.As(callbackResult.err, &startErr) {
			t.Errorf("callback error %T = %v, want *StartError", callbackResult.err, callbackResult.err)
		} else if startErr.Cleanup != CleanupConfirmed || startErr.PID != callbackResult.pid {
			t.Errorf("StartError = %+v, callback PID=%d; want confirmed cleanup and matching PID", startErr, callbackResult.pid)
		}
		eventually(t, func() bool { return supervisor.State().Launches == 1 })
		supervisor.Stop()
		select {
		case <-result:
		case <-time.After(timeoutForCleanupTest):
			t.Fatal("confirmed post-spawn failure did not retry and stop")
		}
		if attempts.Load() < 2 {
			t.Fatalf("Spec called %d times, want retry after confirmed post-spawn failure", attempts.Load())
		}
	})

	t.Run("real post-spawn unknown cleanup cannot retry", func(t *testing.T) {
		var attempts atomic.Int32
		callback := make(chan struct {
			pid int
			err error
		}, 1)
		opts := Options{
			Spec: func(context.Context) (Spec, error) {
				attempts.Add(1)
				spec := helperSpec("sleep-sec", "30")
				spec.AfterStart = func(pid int, err error) {
					callback <- struct {
						pid int
						err error
					}{pid, err}
				}
				return spec, nil
			},
			Restart: RestartPolicy{Mode: RestartAlways},
			Backoff: shortBackoff(),
		}
		supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
			return startWith(ctx, launch, launchHooks{newTree: func(int, *cgroupResources) (*processTree, error) {
				return nil, &StartError{PID: 0, Cleanup: CleanupConfirmed, Err: errInjectedCleanupProbe}
			}})
		})
		if err != nil {
			t.Fatalf("construct supervisor: %v", err)
		}
		outcome := supervisor.Run(context.Background())
		callbackResult := <-callback
		if callbackResult.pid <= 0 {
			t.Errorf("post-spawn callback PID = %d, want real PID", callbackResult.pid)
		}
		var startErr *StartError
		if !errors.As(callbackResult.err, &startErr) {
			t.Errorf("callback error %T = %v, want *StartError", callbackResult.err, callbackResult.err)
		} else if startErr.Cleanup != CleanupUnconfirmed || startErr.PID != callbackResult.pid {
			t.Errorf("StartError = %+v, callback PID=%d; want unconfirmed cleanup and matching PID", startErr, callbackResult.pid)
		}
		if outcome.Reason != CleanupFailed || outcome.Cleanup != CleanupUnconfirmed {
			t.Errorf("outcome = %+v, want CleanupFailed with unconfirmed cleanup", outcome)
		}
		if !errors.Is(outcome.Err, errInjectedCleanupProbe) || !errors.Is(outcome.Err, ErrCleanupUnconfirmed) {
			t.Errorf("outcome error %v lost setup failure or cleanup sentinel", outcome.Err)
		}
		if attempts.Load() != 1 {
			t.Errorf("Spec called %d times, want no retry", attempts.Load())
		}
	})
}

func TestSupervisorUnsafeStartDoesNotReuseEarlierConfirmedExit(t *testing.T) {
	var attempts atomic.Int32
	opts := Options{
		Spec: func(context.Context) (Spec, error) {
			if attempts.Add(1) == 1 {
				return helperSpec("exit", "0"), nil
			}
			return helperSpec("sleep-sec", "30"), nil
		},
		Restart: RestartPolicy{Mode: RestartAlways},
		Backoff: shortBackoff(),
	}
	first := atomic.Bool{}
	supervisor, err := newSupervisor(opts, func(ctx context.Context, launch Spec) (*Child, error) {
		if first.CompareAndSwap(false, true) {
			return startWith(ctx, launch, launchHooks{})
		}
		return startWith(ctx, launch, launchHooks{newTree: func(int, *cgroupResources) (*processTree, error) {
			return nil, errInjectedCleanupProbe
		}})
	})
	if err != nil {
		t.Fatalf("construct supervisor: %v", err)
	}
	outcome := supervisor.Run(context.Background())
	if outcome.Reason != CleanupFailed || outcome.Cleanup != CleanupUnconfirmed {
		t.Fatalf("outcome = %+v, want terminal unconfirmed cleanup", outcome)
	}
	if outcome.Last == nil || outcome.Last.Cleanup != CleanupConfirmed {
		t.Errorf("last exit = %+v, want earlier confirmed exit retained", outcome.Last)
	}
	if !errors.Is(outcome.Err, ErrCleanupUnconfirmed) {
		t.Errorf("outcome error %v does not retain unsafe-start sentinel", outcome.Err)
	}
	if attempts.Load() != 2 {
		t.Errorf("Spec called %d times, want earlier exit and one unsafe start", attempts.Load())
	}
}
