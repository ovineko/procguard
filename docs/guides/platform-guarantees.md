# Platform guarantees

This table is the contract. Orphan prevention and tree handling are always enabled;
`Spec.KillImmediately` skips the grace phase for requested stop and natural-exit
cleanup. There are no syscall-shaped public options.

| ID                       | Guarantee                                   | Linux                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | macOS / BSD                                                                                                                                                                      | Windows                                                                                                                                            |
| ------------------------ | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tree-termination`       | Tree termination                            | New process group; SIGTERM then SIGKILL                                                                                                                                                                                                                                                                                                                                                                                                                                                     | New process group; SIGTERM then SIGKILL                                                                                                                                          | Job Object; TerminateJobObject                                                                                                                     |
| `supervisor-death`       | Supervisor death                            | Leader receives SIGTERM via Pdeathsig; descendants may survive                                                                                                                                                                                                                                                                                                                                                                                                                              | No kernel guarantee for arbitrary children; crashed supervisor leaves children running                                                                                           | Closing the supervisor's Job Object handle kills the assigned tree                                                                                 |
| `graceful-stop`          | Graceful stop                               | Group SIGTERM; all live members share the deadline, even after leader exit                                                                                                                                                                                                                                                                                                                                                                                                                  | macOS: same as Linux. BSD: group SIGTERM, enumerate remaining members, and wait only until the group exits or the grace deadline requires SIGKILL; `KillImmediately` skips grace | No polite signal; entire job gets the grace window, then TerminateJobObject if active; `KillImmediately` terminates the job without the grace wait |
| `natural-exit`           | Natural leader exit                         | SIGTERM live descendants; grace from observed leader exit, then SIGKILL if needed; `KillImmediately` skips the grace and SIGKILLs the group                                                                                                                                                                                                                                                                                                                                                 | macOS: same as Linux. BSD: SIGTERM live descendants and wait from leader exit, then SIGKILL if needed; `KillImmediately` SIGKILLs remaining members without grace                | Active descendants get grace from observed leader exit, then TerminateJobObject if needed; `KillImmediately` terminates the job without grace      |
| `empty-unit`             | Empty-unit detection / Wait                 | procfs pgrp/state scan, excluding leader and zombies; prompt when empty                                                                                                                                                                                                                                                                                                                                                                                                                     | macOS: typed kern.proc.pgrp; BSD: native kern.proc* process records, excluding leader and dead states; prompt when empty                                                         | JobObjectBasicAccountingInformation.ActiveProcesses; prompt at zero                                                                                |
| `exit-killed`            | Exit.Killed                                 | Hard kill needed for any live member                                                                                                                                                                                                                                                                                                                                                                                                                                                        | macOS: same as Linux. BSD: true only when hard kill was needed for a live group member                                                                                           | Hard kill needed for any active job member                                                                                                         |
| `parent-limits`          | Parent-side limits                          | Child Prlimit(RLIMIT_AS), procfs OOM adjustment                                                                                                                                                                                                                                                                                                                                                                                                                                             | Any configured limit fails before launch with ErrUnsupported                                                                                                                     | Any configured limit fails before launch with ErrUnsupported                                                                                       |
| `self-address-space`     | Self address-space limit                    | RLIMIT_AS, clamped to inherited hard limit                                                                                                                                                                                                                                                                                                                                                                                                                                                  | FreeBSD supports it; Darwin reports kernel-dependent acceptance or EINVAL, without promising Linux-equivalent enforcement; OpenBSD reports ErrUnsupported                        | ErrUnsupported                                                                                                                                     |
| `self-oom`               | Self OOM adjustment                         | Writes /proc/self/oom_score_adj                                                                                                                                                                                                                                                                                                                                                                                                                                                             | No-op for compatibility                                                                                                                                                          | No-op for compatibility                                                                                                                            |
| `watch-parent`           | Cooperative WatchParent                     | Immediate PID re-check + polling with ParentExitError; locked-thread kernel SIGTERM; no signal handlers                                                                                                                                                                                                                                                                                                                                                                                     | Immediate PID check + polling with ParentExitError; no signal handlers                                                                                                           | ErrUnsupported; parent-side job covers it                                                                                                          |
| `cgroup-v2`              | Requested cgroup v2 isolation               | Atomic clone-time leaf placement (`CLONE_INTO_CGROUP`, kernel 5.7+); when `MemoryMax > 0`, `memory.max` with `memory.swap.max` pinned to zero where available before child code (a refused pin fails the launch); group SIGTERM then `cgroup.kill` (kernel 5.14+) or a pidfd-pinned recursive PID sweep with subtree membership re-check; without pidfd the sweep is refused in favour of the group kill; recursive liveness from `cgroup.events`, setsid-immune; fail-closed before launch | ErrUnsupported before launch                                                                                                                                                     | ErrUnsupported before launch                                                                                                                       |
| `extra-files`            | ExtraFiles                                  | Supported, starting at descriptor 3                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Supported, starting at descriptor 3                                                                                                                                              | Rejected before launch                                                                                                                             |
| `immediate-stop`         | Immediate hard stop                         | `KillImmediately` sends group SIGKILL without grace; live group members are killed and bounded cleanup/drain completes                                                                                                                                                                                                                                                                                                                                                                      | macOS: same as Linux. BSD: enumerates and SIGKILLs remaining group members without grace; no same-group survivors after Wait                                                     | `KillImmediately` terminates the assigned job without grace; no active job members remain and bounded cleanup/drain completes                      |
| `immediate-natural-exit` | Immediate cleanup after natural leader exit | `KillImmediately` SIGKILLs remaining group members without grace; no survivors after Wait                                                                                                                                                                                                                                                                                                                                                                                                   | macOS: same as Linux. BSD: enumerates and SIGKILLs remaining group members without grace; no survivors after Wait                                                                | `KillImmediately` terminates the job after leader exit without grace; no active members remain                                                     |
| `immediate-cancel`       | Immediate cleanup on cancellation           | Cancelling the launch context SIGKILLs the remaining group without grace; no survivors after Wait                                                                                                                                                                                                                                                                                                                                                                                           | macOS: same as Linux. BSD: enumerates and SIGKILLs remaining group members without grace; no survivors after Wait                                                                | Cancellation terminates the assigned job without grace; no active members remain                                                                   |
| `stdin`                  | Standard input                              | Supported: `*os.File` wires descriptor 0; other readers are copied over a pipe and must eventually return EOF or an error; WaitDelay cannot interrupt a blocked Read                                                                                                                                                                                                                                                                                                                        | Same as Linux                                                                                                                                                                    | Supported through handle inheritance; the same blocked-reader limitation applies                                                                   |
| `setsid-escape`          | Deliberate group escape                     | A descendant calling setsid survives group Stop                                                                                                                                                                                                                                                                                                                                                                                                                                             | Same as Linux                                                                                                                                                                    | Assigned descendants remain in the job                                                                                                             |
| `start-failure`          | Failed launch retries                       | AfterStart once per attempt; backoff advances; Stop joins cleanup                                                                                                                                                                                                                                                                                                                                                                                                                           | Same as Linux                                                                                                                                                                    | Same as Linux                                                                                                                                      |
| `repeated-launches`      | Launch resource ownership                   | Repeated launches release procguard descriptors and locked spawning threads                                                                                                                                                                                                                                                                                                                                                                                                                 | Same ownership; e2e resource accounting is Linux-only                                                                                                                            | Same ownership; e2e resource accounting is Linux-only                                                                                              |
| `pid1-reaping`           | PID 1 orphan reaping                        | No init duties: orphan zombies accumulate across restarts without an init; an init reaps them                                                                                                                                                                                                                                                                                                                                                                                               | Not applicable to this Linux container scenario                                                                                                                                  | Not applicable                                                                                                                                     |

Linux uses `Setpgid` plus `Pdeathsig: SIGTERM`. The kernel signal is tied to the
spawning **OS thread**. procguard keeps a dedicated
locked spawning thread alive until the leader is reaped, including launch-failure
cleanup. A caller may return from its own locked goroutine without signalling the child.
The leader may handle or ignore SIGTERM; this is not an unconditional kernel kill.
Pdeathsig applies to the leader, not automatically to all descendants. Credential
changes may clear the kernel setting. Cooperating Go children can use
[WatchParent](cooperating-children.md) and finish their own cleanup.

`WatchParent` installs no signal handlers. On Unix, inspect detected reparenting
with `var pe *procguard.ParentExitError` and `errors.As(context.Cause(ctx), &pe)`;
`ExpectedPID` and `ObservedPID` identify the mismatch. Linux adds kernel SIGTERM
delivery on top of polling. The consumer controls signal handling, and the first
context cancellation determines the cause. See [cooperating children](cooperating-children.md).

A non-nil `Spec.Cgroup` is an explicit, fail-closed request for Linux cgroup v2
isolation. The leaf is created under `CgroupConfig.Parent` (empty derives the
launcher's own cgroup, which usually cannot host controller limits while the
launcher remains its member) and every unsatisfiable request fails before any
child code runs: no unified v2 mount, kernel below 5.7 (clone3
`CLONE_INTO_CGROUP` is required — post-start migration is not an equivalent),
or an unwritable delegated parent. Placement is atomic at clone and descendants
inherit membership. `MemoryMax == 0` requests isolation without a memory limit
and does not require the memory controller. A positive `MemoryMax` requires that
controller and sets the cap before user code; the leaf's swap is pinned to zero,
making the cap a hard memcg OOM boundary instead of a reclaim hint. A refused pin
fails the launch; only a host without swap control at all proceeds without the
pin (typically swapless).
Graceful stop remains group SIGTERM; the
hard kill prefers `cgroup.kill` (kernel 5.14+, atomic) and otherwise kills
per PID through a pidfd with a membership re-check and bounded
re-enumeration rounds, so a PID reused between enumeration and delivery can
never be signalled and a member forking mid-sweep lands in the next round —
without pidfd the sweep is refused and the process-group kill, whose reuse
safety is the leader-reaping discipline, takes over. When that escalation
still reports an error, the wait ends with it in `Exit.Err` after a bounded
drain instead of holding `Wait` on members the kill could not reach.
Liveness comes from the recursive `populated` state in `cgroup.events`; the
pidfd fallback enumerates `cgroup.procs` in every nested cgroup and re-checks
membership within the launch subtree. A descendant that calls `setsid` or moves
to a nested cgroup still counts and is still killed. Empty nested directories
are removed bottom-up before removing the launch leaf. A subtree that cannot
be emptied is left in place and reported through `Exit.Err`. A cgroup is
not a parent-death mechanism — a killed supervisor leaves the leaf and its
members in place — and a child with write access to cgroup filesystems can
migrate out of it; process groups and cgroups are neither of them security
boundaries.

macOS and BSD have no corresponding arbitrary-child parent-death guarantee here.
If the supervisor crashes, the child can continue running. Cooperating Go children
should use `WatchParent`, which observes reparenting with bounded polling latency.

Windows creates the child suspended, assigns it to a Job Object configured with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, then resumes it. A crash closes the
supervisor's handles and kills the assigned tree; there is no creation-to-assignment
window in which child code can run or spawn. Assignment or resume failures fail
Start and clean up the created process. Job nesting or host job restrictions can
cause assignment failures.

Linux and all four supported BSDs enumerate live process-group members; macOS
uses native process records and Windows polls Job Object accounting. Wait includes
descendant cleanup and can extend to the grace deadline after leader exit, but
adds no grace delay when enumeration shows an empty unit. After escalation, it
waits for live members to disappear (zombies do not count); uninterruptible
kernel operations can delay this. Output draining is additional and bounded by
WaitDelay for inherited pipes. Blocking user writers cannot be interrupted.
Likewise, a non-file `Stdin` reader must eventually return EOF or an error after
the child exits. WaitDelay cannot interrupt its blocked `Read`; such a reader
can keep `Child.Wait` and `Supervisor.Run` blocked after process termination.

`Exit.Cleanup` is `CleanupConfirmed` only when cleanup observed no live unit
members and the leader was validly observed and reaped. Its zero value means
unconfirmed. This process evidence is independent of exit status, I/O errors,
and handle/leaf close diagnostics. A confirmed exit can carry `Exit.Err`; an
unconfirmed terminal result carries `CleanupFailed` and an error wrapping
`ErrCleanupUnconfirmed`. A successful signal by itself is not confirmation.
Failed or malformed runtime enumeration is retained as an error and cannot be
reported as an empty unit.

On macOS a group signal can race process exit and report `EPERM` even when final
membership observation later confirms an empty unit. The signal error remains
in `Exit.Err`; `CleanupConfirmed` independently reports the final cleanup evidence.
It does not claim that the earlier signal was successfully delivered.

`Supervisor.State().Running` consults the current child's exit observation directly.
Once `Child.Exited()` closes it is false, including during descendant cleanup;
this snapshot does not depend on a separate goroutine being scheduled. As with
all process observations, a process can exit immediately after a snapshot.

Unix group signals always precede leader reaping to prevent PID/PGID reuse races.
Windows keeps the job handle open through the grace decision and cleanup; handles
identify the job without numeric PID reuse ambiguity. Unix process groups are not
security boundaries, and Unix descendants can deliberately escape. Windows job
assignment contains descendants from their first instruction; job nesting or host
restrictions may prevent assignment.

Parent-side Linux limits have a start-to-limit window: the child can execute and
allocate before the parent sets limits. Use self-application early in a cooperating
child when this matters. No platform promises an atomic restricted launch.

The Windows immediate-grandchild scenario checks that a descendant spawned by the
assigned child is also terminated when the job closes. Regular lifecycle scenarios
gate on readiness after Start.

The [testing guide](testing.md) maps these stable IDs to native and Docker assertions.
