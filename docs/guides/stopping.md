# Stopping and process trees

A launch and its descendants form one termination unit. `Stop` requests polite
termination and returns immediately; `Wait` returns its cleanup evidence.
Context cancellation makes the same request. The default grace period is ten seconds;
negative durations are invalid. Calling Stop repeatedly never extends it.
Set `Spec.KillImmediately` to skip the graceful phase for both requested stops
and natural leader exits with remaining descendants: the termination unit is
hard-killed without waiting through `GracePeriod`. On Unix, this is a group
SIGKILL; on Windows, it terminates the job. Cleanup still waits for exit and
membership observation, and an uninterruptible process may delay completion.
The output-drain allowance is two seconds in immediate mode instead of the
grace period plus two seconds.

Start failures still kill and reap any process created by that attempt.
Output-drain bounds continue to apply.

On Unix, the leader and all descendants that remain in its process group are one
unit. When Stop or cancellation is acted on, procguard sends
SIGTERM to the group and starts `GracePeriod`. The same deadline applies even if
the leader exits immediately. If all live members exit before it, no SIGKILL is
sent. At the deadline, any live members are killed with one group SIGKILL.

If the leader exits on its own, procguard checks for live descendants. If any
remain, it sends SIGTERM and gives them `GracePeriod` from the observed leader
exit before escalating. With no descendants, completion stays prompt even with
a long grace period. Group membership is polled every 25ms, excluding the
unreaped leader and non-running dead processes. Linux scans procfs; macOS and
the other BSDs query native process records through sysctl.

On Windows, the Job Object is the unit. There is no polite signal for arbitrary
non-console programs, so Stop permits voluntary exit until the grace deadline.
After a natural leader exit, remaining job processes get the same grace window.
The job's active process count is polled every 25ms. Only a nonempty job at the
deadline is terminated; the job handle stays open until this decision and cleanup.

`Wait` and `Done` complete after unit cleanup and output draining. On all
supported platforms, lingering descendants may delay completion up to the grace
deadline, while an empty unit adds no grace delay. After a hard kill, procguard
waits for no live members to remain; an uninterruptible kernel operation can take
longer. `Exit.Killed` records a hard-kill attempt for the termination unit,
including descendants after the leader exited. If enumeration fails at runtime,
the error is returned in `Exit.Err`; cleanup still attempts termination according
to the selected policy and does not report an unsupported probe as success.
`Exit.Cleanup` reports process-unit evidence independently of `Exit.Err`:
only `CleanupConfirmed` means a final membership observation found no live
descendants and the leader was validly observed and reaped. The zero value means
unknown, not safe. A confirmed exit may still have I/O, close, or probe
diagnostics, and a nonzero/signalled process can be confirmed. Conversely,
successful signal delivery alone does not confirm cleanup. Consumers should
inspect `Cleanup` before treating `Wait` as proof that the unit is gone.

Repeated Stop calls never restart a grace window already in progress.

After the leader exits, a failed hard kill limits descendant draining to two
seconds and is retained in `Exit.Err`, including with `KillImmediately`. This
does not bound ownership of a still-live leader or interrupt a blocked caller
reader or writer.

`Signal` always targets the leader only. This matters for diagnostics such as
SIGUSR1: a grandchild must not receive an application-specific diagnostic request.
An absent or finished child returns `os.ErrProcessDone`. Windows supports the
signals supported by `os.Process.Signal`; arbitrary Unix signals are unavailable.

Unix exit observation uses Linux `waitid(WNOWAIT)` or BSD/macOS kqueue. The leader
is not reaped until the last group signal, keeping its PID reserved. This prevents
a delayed cleanup signal from targeting an unrelated process group whose PGID
reused that PID. No timer survives completion and no group signal occurs after
reaping. Do not install an external reaper that calls wait on procguard's children.

These guarantees cover descendants that remain in the group or job. On Unix,
a descendant that deliberately changes session or process group can escape;
this library is not a containment or security boundary. SIGKILL cannot immediately
finish a process stuck in an uninterruptible kernel operation, and descendants
may remain as non-running zombies until their adopting parent reaps them.

See the [platform contract](platform-guarantees.md) for supervisor-crash behavior.
