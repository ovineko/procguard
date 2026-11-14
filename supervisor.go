package procguard

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"time"
)

// RestartMode assigns meaning only to success versus failure; applications own
// any richer exit-code protocol.
type RestartMode int

// RestartAlways is the zero-value policy, including for successful exits.
const (
	RestartAlways RestartMode = iota
	RestartOnFailure
	RestartNever
)

// RestartPolicy controls automatic restarts; explicit Restart overrides Mode.
// PreventExitCodes therefore always ends supervision for a matching exit.
type RestartPolicy struct {
	Mode             RestartMode
	PreventExitCodes []int
}

// Options is copied at construction. Spec runs for every attempt, outside the
// supervisor lock; it should honor its context. A nil Backoff gets private defaults.
// EventBuffer defaults to 64; a negative value is invalid.
type Options struct {
	Spec        func(ctx context.Context) (Spec, error)
	Restart     RestartPolicy
	Backoff     *Backoff
	EventBuffer int
}

// OutcomeReason separates explicit stop, cancellation, policy completion and unsafe cleanup.
type OutcomeReason int

// CleanupFailed takes precedence over all other reasons; with confirmed cleanup,
// Stopped takes precedence over Cancelled and Completed.
const (
	Stopped OutcomeReason = iota
	Cancelled
	Completed
	CleanupFailed
)

// Outcome is authoritative even when observational events were dropped.
type Outcome struct {
	Reason  OutcomeReason
	Last    *Exit
	Cleanup CleanupState
	Err     error
}

// State is a detached snapshot. Running reports whether the current leader
// process is alive; it turns false as soon as the leader exits, while descendant
// cleanup may still be in progress, so it never reports a dead process as running.
// PID and StartedAt keep describing that launch until its exit is recorded.
// Restarts counts approved relaunches after exits; spawn retries affect
// Failures, but do not increment Restarts or Launches.
type State struct {
	Running                      bool
	PID                          int
	StartedAt                    time.Time
	Launches, Restarts, Failures int
	NextDelay                    time.Duration
	Last                         *Exit
	DroppedEvents                uint64
}

// EventKind describes a transition; events are not an acknowledgement protocol.
type EventKind int

// EventStopped is emitted for every terminal outcome, including completion.
const (
	EventStarted EventKind = iota
	EventStartFailed
	EventExited
	EventRestartScheduled
	EventStopped
)

// Event carries a detached exit snapshot when associated with a child exit.
type Event struct {
	Kind  EventKind
	Time  time.Time
	PID   int
	Exit  *Exit
	Delay time.Duration
	Err   error
}

// Supervisor manages one logical child. Create independent instances for
// independent binaries. It must not be copied and permits exactly one Run.
type Supervisor struct {
	opts                       Options
	start                      func(context.Context, Spec) (*Child, error)
	mu                         sync.Mutex
	ran, finished, stopped     bool
	stable, restart, immediate bool
	launchStarted              bool
	child                      *Child
	cancel                     context.CancelFunc
	stop                       chan struct{}
	wake                       chan struct{}
	events                     chan Event
	state                      State
	terminalCleanup            CleanupState
	terminalErr                error
	testHookSpawnWindow        func()
}

// NewSupervisor validates configuration without launching a child.
func NewSupervisor(opts Options) (*Supervisor, error) {
	return newSupervisor(opts, Start)
}

func newSupervisor(opts Options, start func(context.Context, Spec) (*Child, error)) (*Supervisor, error) {
	if opts.Spec == nil {
		return nil, errors.New("Spec factory is required")
	}
	if start == nil {
		return nil, errors.New("start function is required")
	}
	if opts.Restart.Mode < RestartAlways || opts.Restart.Mode > RestartNever {
		return nil, errors.New("invalid restart mode")
	}
	if opts.EventBuffer < 0 {
		return nil, errors.New("event buffer must not be negative")
	}
	if opts.EventBuffer == 0 {
		opts.EventBuffer = defaultEventBuffer
	}
	if opts.Backoff == nil {
		opts.Backoff = NewBackoff(0, 0)
	}
	opts.Restart.PreventExitCodes = slices.Clone(opts.Restart.PreventExitCodes)
	return &Supervisor{opts: opts, start: start, terminalCleanup: CleanupConfirmed, stop: make(chan struct{}), wake: make(chan struct{}, 1), events: make(chan Event, opts.EventBuffer)}, nil
}

// Run waits for final child cleanup and closes Events. A second call panics.
// A Spec factory or AfterStart callback that blocks indefinitely also blocks Run.
func (s *Supervisor) Run(ctx context.Context) Outcome {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.ran {
		s.mu.Unlock()
		panic("procguard: Run called more than once")
	}
	s.ran, s.cancel = true, cancel
	if s.stopped {
		cancel()
	}
	s.mu.Unlock()
	for {
		if s.shouldStop(ctx) {
			return s.finish(ctx)
		}
		child, err := s.launch(runCtx)
		if err != nil {
			s.mu.Lock()
			s.emit(Event{Kind: EventStartFailed, Err: err})
			var startErr *StartError
			unsafe := errors.Is(err, ErrCleanupUnconfirmed) || (errors.As(err, &startErr) && startErr.Cleanup != CleanupConfirmed)
			if unsafe {
				s.terminalCleanup = CleanupUnconfirmed
				s.terminalErr = errors.Join(err, ErrCleanupUnconfirmed)
				s.mu.Unlock()
				return s.finish(ctx)
			}
			s.mu.Unlock()
			if s.shouldStop(ctx) {
				return s.finish(ctx)
			}
			s.waitDelay(ctx, s.opts.Backoff.Next())
			continue
		}
		exit := child.Wait()
		s.mu.Lock()
		s.child = nil
		s.state.Running, s.state.PID, s.state.StartedAt = false, 0, time.Time{}
		s.state.Last = cloneExit(&exit)
		s.emit(Event{Kind: EventExited, PID: exit.PID, Exit: &exit})
		if exit.Cleanup != CleanupConfirmed {
			s.terminalCleanup = CleanupUnconfirmed
			s.terminalErr = errors.Join(exit.Err, ErrCleanupUnconfirmed)
			s.mu.Unlock()
			return s.finish(ctx)
		}
		if s.stopped || ctx.Err() != nil {
			s.mu.Unlock()
			return s.finish(ctx)
		}
		if !s.shouldRestart(exit) {
			s.mu.Unlock()
			return s.finish(ctx)
		}
		s.state.Restarts++
		delay := s.restartDelay(exit)
		s.mu.Unlock()
		s.waitDelay(ctx, delay)
	}
}

// Stop may precede Run. Once requested, neither Restart nor any later exit can
// cause a relaunch. A spawn in progress is cancelled and joined by Run.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.finished {
		return
	}
	s.stopped = true
	close(s.stop)
	if s.cancel != nil {
		s.cancel()
	}
	if s.child != nil {
		s.child.Stop()
	}
}

// Restart coalesces concurrent requests. Any immediate request wins, clears
// backoff, and skips delay. During a backoff wait either form wakes it immediately.
func (s *Supervisor) Restart(immediate bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.finished {
		return
	}
	s.restart, s.immediate = true, s.immediate || immediate
	if s.child != nil {
		s.child.Stop()
	} else {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// MarkStable affects the current launch, including its successful AfterStart,
// and clears no counters
// until that launch exits. It is a no-op while no child is running.
func (s *Supervisor) MarkStable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child != nil || s.launchStarted {
		s.stable = true
	}
}

// Signal returns os.ErrProcessDone when there is no current child.
func (s *Supervisor) Signal(sig os.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return os.ErrProcessDone
	}
	return s.child.Signal(sig)
}

// State remains usable after Run returns; callers may modify its Last safely.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	if s.child != nil {
		select {
		case <-s.child.Exited():
			state.Running = false
		default:
		}
	}
	state.Last = cloneExit(state.Last)
	state.Failures = s.opts.Backoff.Failures()
	return state
}

// Events is bounded and lossy. Full buffers drop the newest event and increment
// State.DroppedEvents. Use State and Outcome for correctness.
func (s *Supervisor) Events() <-chan Event { return s.events }

func (s *Supervisor) launch(ctx context.Context) (*Child, error) {
	s.mu.Lock()
	s.stable, s.launchStarted = false, false
	if s.restart && s.immediate {
		s.opts.Backoff.Reset()
	}
	s.restart, s.immediate = false, false
	select {
	case <-s.wake:
	default:
	}
	s.mu.Unlock()
	if s.testHookSpawnWindow != nil {
		s.testHookSpawnWindow()
	}
	spec, err := s.opts.Spec(ctx)
	if err != nil {
		return nil, err
	}
	afterStart := spec.AfterStart
	spec.AfterStart = func(pid int, startErr error) {
		s.mu.Lock()
		s.launchStarted = startErr == nil
		s.mu.Unlock()
		if afterStart != nil {
			afterStart(pid, startErr)
		}
	}
	child, err := s.start(ctx, spec)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.child = child
	s.launchStarted = false
	s.state.Running, s.state.PID, s.state.StartedAt = true, child.PID(), child.StartedAt()
	s.state.Launches++
	s.state.NextDelay = 0
	// Stop publishes a persistent flag before cancelling. Re-check after publishing
	// the child, so a stop during Spec, Start, or AfterStart cannot be lost.
	if s.stopped || ctx.Err() != nil || s.restart {
		child.Stop()
	}
	s.emit(Event{Kind: EventStarted, PID: child.PID()})
	s.mu.Unlock()
	return child, nil
}

func (s *Supervisor) shouldStop(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped || ctx.Err() != nil
}

func (s *Supervisor) shouldRestart(exit Exit) bool {
	policy := s.opts.Restart
	return !slices.Contains(policy.PreventExitCodes, exit.Code) &&
		(s.restart || (policy.Mode != RestartNever && (policy.Mode != RestartOnFailure || exit.Code != 0)))
}

func (s *Supervisor) restartDelay(exit Exit) time.Duration {
	immediate := s.immediate
	s.restart, s.immediate = false, false
	select {
	case <-s.wake:
	default:
	}
	if immediate || s.stable || s.opts.Backoff.Stable(exit.Runtime()) {
		s.opts.Backoff.Reset()
	}
	s.stable = false
	if immediate {
		return 0
	}
	return s.opts.Backoff.Next()
}

func (s *Supervisor) waitDelay(ctx context.Context, delay time.Duration) {
	s.mu.Lock()
	s.state.NextDelay = delay
	s.emit(Event{Kind: EventRestartScheduled, Delay: delay})
	s.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.stop:
	case <-ctx.Done():
	case <-s.wake:
	case <-timer.C:
	}
	s.mu.Lock()
	if s.restart {
		if s.immediate {
			s.opts.Backoff.Reset()
		}
		s.restart, s.immediate = false, false
	}
	select {
	case <-s.wake:
	default:
	}
	s.state.NextDelay = 0
	s.mu.Unlock()
}

func (s *Supervisor) finish(ctx context.Context) Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := Completed
	if s.stopped {
		reason = Stopped
	} else if ctx.Err() != nil {
		reason = Cancelled
	}
	cleanup := CleanupConfirmed
	terminalErr := s.terminalErr
	if s.terminalCleanup != CleanupConfirmed {
		cleanup = CleanupUnconfirmed
		reason = CleanupFailed
		terminalErr = errors.Join(terminalErr, ErrCleanupUnconfirmed)
	} else if s.state.Last != nil {
		cleanup = s.state.Last.Cleanup
		terminalErr = s.state.Last.Err
	}
	s.finished, s.state.NextDelay = true, 0
	s.emit(Event{Kind: EventStopped, Exit: s.state.Last, Err: terminalErr})
	close(s.events)
	return Outcome{Reason: reason, Last: cloneExit(s.state.Last), Cleanup: cleanup, Err: terminalErr}
}

func (s *Supervisor) emit(event Event) {
	event.Time = time.Now()
	event.Exit = cloneExit(event.Exit)
	select {
	case s.events <- event:
	default:
		s.state.DroppedEvents++
	}
}

func cloneExit(exit *Exit) *Exit {
	if exit == nil {
		return nil
	}
	copied := *exit
	return &copied
}
