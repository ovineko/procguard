# Resource limits

`Limits{AddressSpace: bytes}` requests the platform's RLIMIT_AS limit. On Linux
this is an address-space ceiling, not an RSS ceiling. Zero means unchanged.
Negative values are rejected. The requested soft limit is clamped to the inherited
hard limit; the hard limit is never raised or lowered. FreeBSD uses signed rlimit
fields, handled separately from unsigned platforms.

`Spec.Limits` is applied by the parent immediately after launch. On Linux it uses
`Prlimit` on the child PID and writes `/proc/<pid>/oom_score_adj`. A pointer in
`OOMScoreAdj` distinguishes unset from an explicit zero. Positive values make a
process more eligible for the OOM killer; negative values may require privilege.
Kernel validation and permission errors are returned to the caller.

On every other OS, any configured parent-side limit returns an error wrapping
`ErrUnsupported` before creating a process. Use `errors.Is(err, procguard.ErrUnsupported)`.
If Linux setup fails after creation, Start kills and reaps the launch before
returning its error. `AfterStart` still runs exactly once with the error.

The child executes between creation and the parent's limit calls. These calls are
not atomic, and multiple limits are not transactional. A cooperating child can
instead call `ApplyLimitsToSelf` early. On FreeBSD this keeps RLIMIT_AS effective
without a parent-side setter. Darwin reports the kernel's result: some versions
accept the request, while others reject it with EINVAL. Acceptance and a matching
`getrlimit` value do not establish Linux-equivalent memory enforcement on macOS.
OpenBSD and Windows return ErrUnsupported. Off Linux self OOM adjustment is
intentionally a no-op for compatibility.

Very small address-space limits can prevent Go's runtime from operating, and the
race detector reserves substantial virtual memory. Unit tests avoid small Darwin
limits and lowering under the race detector. Native e2e observes Darwin's actual
soft/hard values, requiring either the exact accepted target or EINVAL with an
unchanged limit; unrelated errors and unverified success fail the check.
