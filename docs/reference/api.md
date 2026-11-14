# API reference

Import `github.com/ovineko/procguard`. Handles are safe for concurrent method calls
and must not be copied. Options and launch inputs must not be mutated while in use.
`PreventExitCodes` is copied at construction. Shared output writers must support
the consumer's intended concurrent use.

## One launch

```go
type Spec struct {
    Path string
    Args []string
    Dir string
    Env []string
    Stdin io.Reader
    Stdout, Stderr io.Writer
    ExtraFiles []*os.File
    GracePeriod time.Duration
    KillImmediately bool
    Limits Limits
    AfterStart func(pid int, err error)
    Cgroup *CgroupConfig
}
func Start(ctx context.Context, spec Spec) (*Child, error)
func (c *Child) PID() int
func (c *Child) StartedAt() time.Time
func (c *Child) Signal(sig os.Signal) error
func (c *Child) Stop()
func (c *Child) Wait() Exit
func (c *Child) Exited() <-chan struct{} // the leader exited; descendant cleanup may still run
func (c *Child) Done() <-chan struct{}   // unit cleanup and output draining have completed

type CleanupState uint8
const (
    CleanupUnconfirmed CleanupState = iota
    CleanupConfirmed
)
var ErrCleanupUnconfirmed error
type StartError struct {
    PID int
    Cleanup CleanupState
    Err error
}
func (e *StartError) Error() string
func (e *StartError) Unwrap() error
type Exit struct {
    PID int
    Code int
    Signal os.Signal
    StartedAt, ExitedAt time.Time
    Requested bool
    Killed bool
    Cleanup CleanupState
    Err error
}
func (e Exit) Runtime() time.Duration
```

`CleanupConfirmed` is the only state that confirms the launch's process unit is
empty and its leader has been reaped. The zero value and unknown values are
unconfirmed. This says nothing about child success, output draining, handle or
cgroup-leaf closure, or application data. A confirmed exit may still carry
`Err` (for example, an output-writer failure); a nonzero or signalled exit may
also be confirmed. `ErrCleanupUnconfirmed` is joined into unsafe terminal errors
so `errors.Is` identifies them without hiding underlying causes.

`StartError` describes failed starts after cleanup. `PID` is the actual created
PID, or zero when no process was created; no-process failures are confirmed.
For a created process, the cleanup state reports observed evidence, not merely
successful signal delivery. `Unwrap` preserves the start and cleanup causes.

`Child` has no exported fields; obtain it from Start. See
[process lifecycle](../guides/process-lifecycle.md) and [stopping](../guides/stopping.md).

## Cgroup isolation

```go
type CgroupConfig struct {
    Parent string
    MemoryMax int64
}
```

`Spec.Cgroup == nil` uses the platform's default termination unit. A non-nil
value requests Linux cgroup v2 isolation and fails before launch if the request
cannot be satisfied; other platforms return `ErrUnsupported`.

`Parent` names the delegated cgroup v2 directory under which each launch creates
its private leaf. Relative paths are resolved against the cgroup v2 mount. An
empty value derives the launcher's own cgroup, which usually cannot enable
controllers while the launcher remains a member; use a delegated parent when
requesting limits.

`MemoryMax` is the whole-tree memory cap in bytes, enforced before child code
runs. Zero requests isolation without a memory limit; negative values are invalid.
A positive value requires the memory controller and pins the leaf's swap limit
to zero when swap control exists. A refused pin fails the launch. This is cgroup
memory accounting, not RSS or `RLIMIT_AS`.

See [platform guarantees](../guides/platform-guarantees.md) for kernel requirements,
termination behavior, and platform-specific containment limitations.

## Limits and parent watching

```go
type Limits struct {
    AddressSpace int64
    OOMScoreAdj *int
}
var ErrUnsupported error
func ApplyLimitsToSelf(l Limits) error
func WatchParent(ctx context.Context, parentPID int, pollInterval time.Duration) (context.Context, context.CancelFunc, error)

type ParentExitError struct {
    ExpectedPID int // The parentPID passed to WatchParent.
    ObservedPID int // Parent PID at detection time, typically 1 or a subreaper.
}
func (e *ParentExitError) Error() string
```

Use `errors.Is(err, ErrUnsupported)` to identify a missing capability.
On error WatchParent returns nil context and cancel. Unix detects parent PID
changes immediately and by polling, cancelling with a `*ParentExitError` cause:

```go
var pe *procguard.ParentExitError
if errors.As(context.Cause(ctx), &pe) {
    log.Printf("parent process died (expected %d, now %d)", pe.ExpectedPID, pe.ObservedPID)
}
```

`ctx.Err()` is still `context.Canceled`. The returned cancel function uses the
ordinary `context.Canceled` cause; input context cancellation preserves its cause.
The first cancellation wins. Linux adds kernel SIGTERM delivery on top of polling;
procguard installs no signal handlers. Consumers decide how to handle SIGTERM.
Windows returns ErrUnsupported. See
[resource limits](../guides/resource-limits.md) and
[cooperating children](../guides/cooperating-children.md).

## Backoff

```go
func NewBackoff(maximum, stableAfter time.Duration) *Backoff
func (b *Backoff) Next() time.Duration
func (b *Backoff) Current() time.Duration
func (b *Backoff) Reset()
func (b *Backoff) Failures() int
func (b *Backoff) Saturated() bool
func (b *Backoff) Stable(runtime time.Duration) bool
```

`Backoff` has no exported fields. Its zero value is usable. Defaults are a
60-second maximum and five-minute stability threshold. The first delay is one
second, capped at the maximum. Configuration is immutable after construction.

## Restarting supervision

```go
type RestartMode int
const (
    RestartAlways RestartMode = iota
    RestartOnFailure
    RestartNever
)
type RestartPolicy struct {
    Mode RestartMode
    PreventExitCodes []int
}
type Options struct {
    Spec func(ctx context.Context) (Spec, error)
    Restart RestartPolicy
    Backoff *Backoff
    EventBuffer int
}
func NewSupervisor(opts Options) (*Supervisor, error)
func (s *Supervisor) Run(ctx context.Context) Outcome
func (s *Supervisor) Stop()
func (s *Supervisor) Restart(immediate bool)
func (s *Supervisor) MarkStable()
func (s *Supervisor) Signal(sig os.Signal) error
func (s *Supervisor) State() State
func (s *Supervisor) Events() <-chan Event

type OutcomeReason int
const (
    Stopped OutcomeReason = iota
    Cancelled
    Completed
    CleanupFailed
)
type Outcome struct {
    Reason OutcomeReason
    Last *Exit
    Cleanup CleanupState
    Err error
}
type State struct {
    Running bool // the current leader process is alive; false as soon as it exits
    PID int
    StartedAt time.Time
    Launches, Restarts, Failures int
    NextDelay time.Duration
    Last *Exit
    DroppedEvents uint64
}
type EventKind int
const (
    EventStarted EventKind = iota
    EventStartFailed
    EventExited
    EventRestartScheduled
    EventStopped
)
type Event struct {
    Kind EventKind
    Time time.Time
    PID int
    Exit *Exit
    Delay time.Duration
    Err error
}
```

`Outcome.Cleanup` is the authoritative process-unit confirmation, including a
failed start with no `Last`. Only `CleanupConfirmed` authorizes a next
generation. `CleanupFailed` wins over stop/cancellation whenever terminal
cleanup is unknown, and its `Outcome.Err` wraps `ErrCleanupUnconfirmed` while
preserving underlying causes. For confirmed outcomes, `Err` may still contain
I/O or close diagnostics. Ordinary confirmed nonzero exit status remains in
`Last.Code`, separate from errors; such diagnostics do not negate process
confirmation.

`Supervisor` has no exported fields. Only one Run is permitted; repeated calls
panic. Default mode is RestartAlways, default event capacity is 64, and a nil
backoff allocates a private default. There is no global child registry.
Signal with no current child returns `os.ErrProcessDone`.

The event prefix distinguishes EventStopped from outcome reason Stopped.
See [supervision](../guides/supervision.md) for decision order and race behavior,
and [events and state](../guides/events-and-state.md) for observational events.
