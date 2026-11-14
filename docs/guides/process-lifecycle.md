# Process lifecycle

`Start(ctx, spec)` creates one launch. `Args` excludes argv[0]. `Dir` selects the
working directory. A nil `Env` inherits the parent environment; a non-nil slice
replaces it, following `exec.Cmd` rules. procguard does not filter environment names.
Nil `Stdout` and `Stderr` discard output. Pass `os.Stdout` and `os.Stderr` explicitly
to inherit them. Nil `Stdin` disconnects input. An `*os.File` given as `Stdin` wires
the child's standard input directly; any other reader is copied over a pipe.
`WaitDelay` bounds pipe draining, but cannot interrupt a reader blocked in `Read`:
pass the read end of an `os.Pipe`, or another reader that returns EOF or an error
once the child exits. If `Stdout` and `Stderr`
are the same writer, at most one copy goroutine writes to it at a time. No output
capture or logging policy is built in.

`Start` returns after process creation, containment setup, limits, and `AfterStart`.
`AfterStart(pid, err)` runs synchronously exactly once, even when validation fails,
and receives the typed `*StartError` on failure, carrying the PID, cleanup
evidence, and underlying cause. PID is zero when nothing was created; it can be
nonzero when setup or limits fail. A failure before process creation is
`CleanupConfirmed`; after creation, confirmation requires observed cleanup and
reaping, not just successful kill. Nil error means launch succeeded. Cancellation
can stop that successful launch immediately afterward.

On Unix, `ExtraFiles` supplies descriptors 3 and up. The parent retains ownership
of every file. Close the parent's per-launch copies in `AfterStart`, including on
failure; a child keeps its inherited copies. The callback can start serving pipes.
Windows rejects nonempty `ExtraFiles` before launching. Callbacks and factories
must return promptly and must not wait for their own supervisor's `Run` to finish.

`PID` and `StartedAt` are immutable. `Done` closes after cleanup and output draining.
`Wait` is safe to call repeatedly or concurrently and returns a value snapshot.
`Exit.Code` is -1 for Unix signal exits; `Exit.Signal` identifies the signal.
A plain nonzero exit is a status, not `Exit.Err`. Other wait, setup observation,
termination, or output-copy failures appear in `Err`.

`Requested` means a stop request was recorded before observed completion.
`Killed` means a hard kill was needed for any member of the unit.
`CleanupConfirmed` means the final observation found no live descendants and the
leader's exit and reaping were confirmed; the zero state is unknown. It is
independent of status and diagnostics: nonzero/signalled exits can be confirmed,
and confirmed exits can still carry I/O, probe, or close errors.
`Runtime` measures launch to observed leader exit, excluding descendant cleanup
and output draining. `Wait` may be delayed up to `GracePeriod` by descendants;
an empty unit completes promptly when membership enumeration succeeds. See the
[stop semantics](stopping.md) for details.
`WaitDelay` is the grace period plus two seconds, or just two seconds when
`KillImmediately` is set. Its output-drain timer starts inside `cmd.Wait`, after
unit cleanup and reaping, so even a natural exit followed by a full descendant
grace window cannot block termination on inherited pipes.
An escaped descendant can add this drain delay after the grace window. User
writers must return from `Write`; procguard cannot interrupt an arbitrary blocking
writer.

Piping between children uses one `os.Pipe`: give the source child's `Stdout` the
write end and the destination child's `Stdin` the read end. os/exec wires both
descriptors directly, so data flows without copy goroutines. Close the parent's
write-end copy as soon as the source starts: otherwise the destination never sees
EOF, even after the source exits. Close the parent's read-end copy as soon as the
destination starts: otherwise an early destination exit leaves the parent's copy
as the only reader, the source's writes keep succeeding into the pipe, and the
source can block on a full pipe forever instead of failing with EPIPE. With both
copies closed, the destination's early exit turns the source's next write into an
EPIPE failure, which is the abort path. procguard adds no buffering of its own.
