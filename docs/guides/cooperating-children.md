# Cooperating children

An arbitrary executable needs no library integration. A Go child that wants to
notice parent death or apply its own limits can use `WatchParent` and
`ApplyLimitsToSelf`. Pass the expected supervisor PID through your own argument
or environment protocol. Reading the current parent PID only after startup can
miss an earlier reparenting; the original expected PID is what enables the race check.

`WatchParent(ctx, expectedPID, interval)` returns a derived context and cancel
function, or an error. Parent PID must be positive. A zero interval defaults to
five seconds; accepted intervals range from 500ms through 60s, including endpoints.
Validation applies on every platform. Always call the returned cancel function
when monitoring is no longer needed. On error the context and cancel are nil.

On Unix, monitoring immediately checks `getppid` against the expected PID and
polls at the configured interval. A mismatch cancels the context with a
`*procguard.ParentExitError` cause containing the expected PID and the parent PID
observed at detection time (typically 1 or a subreaper). Inspect it after cancellation:

```go
var pe *procguard.ParentExitError
if errors.As(context.Cause(ctx), &pe) {
    log.Printf("parent process died (expected %d, now %d)", pe.ExpectedPID, pe.ObservedPID)
}
```

Here `ctx` is the context returned by `WatchParent`. Its `Err()` remains
`context.Canceled`. Calling the returned cancel function gives the ordinary
`context.Canceled` cause; cancellation of the input context preserves that
context's cause. The first cancellation wins, so an input context cancelled
before detection keeps its cause.

On Linux, monitoring also sets `PR_SET_PDEATHSIG` to SIGTERM from a dedicated
locked OS thread, then immediately re-checks the parent PID to close the setup
race. The kernel stores this attribute per thread; the thread stays locked for
the watch's lifetime and clears the setting when monitoring ends. A prctl failure
is returned instead of being silently logged.

procguard installs no signal handlers. Linux kernel SIGTERM delivery is to the
process, and the consumer's own signal handling decides how to respond. Without
a consumer handler, SIGTERM normally terminates the process. SIGTERM itself never
cancels the watch context; PID polling supplies the parent-exit cause. A consumer
that wants to observe that cause must allow detection to happen before cancelling
the input context or exiting in its own signal handler.

Windows returns ErrUnsupported; the parent's Job Object provides tree cleanup
instead.

Call `ApplyLimitsToSelf(procguard.Limits{AddressSpace: 2 << 30})` early in a child
to request a two-GiB address-space soft limit, subject to the inherited hard limit.
Check the returned error, including ErrUnsupported, and let your application
choose its own fallback. The [limits guide](resource-limits.md) details each OS.
