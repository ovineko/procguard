# Events and state

Events are observational. They must never block supervision. The channel has a
bounded buffer (64 by default); when full, the newest event is dropped and
`State.DroppedEvents` increments. `EventBuffer` may be positive or zero for the
default; negative values are rejected. Run closes the channel after its final event.

Kinds are `EventStarted`, `EventStartFailed`, `EventExited`,
`EventRestartScheduled`, and `EventStopped`. Start failures carry `Err`, exits
carry an `Exit` snapshot, and scheduled restarts carry `Delay`. `EventStopped`
means supervision ended, including policy completion and context cancellation;
use `Outcome.Reason` to distinguish them.

Use `State` and `Outcome` for correctness. `State` includes the current PID and
start time, successful launch count, approved restart count, backoff failure
streak, next delay, latest exit, and dropped-event count. PID and StartedAt are
zero while no child is running. NextDelay is the scheduled duration, not a
continuously updated countdown. Returned exit pointers are detached copies.

Events can be dropped even if you eventually drain the channel. Never depend on
receiving a particular event to release essential resources; use `AfterStart`
for per-launch resources, `Child.Done` for launch completion, and `Run` for final
supervision completion. An event may already be outdated by the time it is read.
No logger or metrics framework is required; consumers choose how to publish observations.

## Running versus cleaning up

`State.Running` describes the leader process. It turns false the moment the leader
exits, even though descendant cleanup can keep the launch busy for up to `GracePeriod`
before its exit is recorded in `State.Last`. Use `Running` together with `StartedAt`
to judge whether a launch has been up for some duration: a crashed process never
counts as running while its descendants are being cleaned up. `Child.Exited` gives
the same moment at the single-launch layer. `Child.Done` and `Child.Wait` wait for
the whole unit.
