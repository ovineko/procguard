# Testing

procguard tests observable process behavior. Readiness uses files and bounded
polling; cleanup timers model work, not synchronization. The fixture does not pin
its consumer's spawning thread. All e2e code stays in the root Go module and adds
no dependencies.

## Layers and commands

| Layer                        | Command                                                                                          | Scope                                                 |
| ---------------------------- | ------------------------------------------------------------------------------------------------ | ----------------------------------------------------- |
| Unit and process integration | `go test -count=1 ./...`                                                                         | API behavior, launch races, signals, syscall failures |
| Race detector                | `go test -race -count=3 ./...`                                                                   | Concurrent state and cleanup ownership                |
| Native e2e                   | `go test -tags e2e -count=1 -v ./e2e`                                                            | A separate fixture binary on the current OS           |
| Docker e2e                   | `PROCGUARD_E2E_REQUIRE=1 go test -tags e2e_docker -count=1 -v ./e2e/docker`                      | Linux deployment profiles, shared assertions          |
| Optional emulation           | `PROCGUARD_E2E_REQUIRE=1 PROCGUARD_E2E_QEMU=1 go test -tags e2e_docker -count=1 -v ./e2e/docker` | Additional arm64/riscv64 smoke profiles               |

The corresponding tasks are `task e2e`, `task e2e:docker`, and
`task e2e:docker:qemu`. Without `PROCGUARD_E2E_REQUIRE=1`, a missing Docker CLI or
unreachable daemon produces an explicit skip. The task and CI set it to fail.
Docker must be local to the filesystem holding the bind-mounted fixture.
Use `-run 'TestProfiles/nonroot'` to narrow a Docker failure.

For the direct lint command, set `GOLANGCI_LINT_CACHE` to a writable cache
directory when its default cache is read-only.

The repository toolchain uses `@shibanet0/datamitsu-config` 0.6.3. After
`pnpm install --frozen-lockfile`, run `pnpm dm init` and `pnpm dm check`.
`datamitsu.config.js` pins golangci-lint 2.15.0 and selects Go 1.27.0 for its
embedded ruleguard analyzer. The launcher derives `GOROOT` from that SDK rather
than inheriting a machine path; Go downloads and verifies the SDK on first use
if needed. Build and test commands still use the caller's Go version. The root
`.datamitsuignore` records intentionally disabled tools and their scope.
The lint gate includes both `e2e` and `e2e_docker` build tags, so the fixture,
shared assertions, and Docker runner receive the same Go checks as the library.

The development-only npm dependency graph includes `braces` 3.0.3, affected by
GHSA-vfj7-8cjw-p6xm with no fixed upstream release. It is used by the toolchain's
glob matcher, not linked into procguard or its consumer binaries. Repository
vulnerability scanners are disabled for that graph; this is not a claim that
the dependency is patched.

## Fixture protocol

The native harness builds `e2e/fixture` into a test temporary directory. Docker
cross-builds it once per architecture with `CGO_ENABLED=0`, bind-mounts the binary
read-only, and uses the Docker CLI without an SDK. Images are pinned by tag, not
digest. Each container is removed in teardown, including after failure.

`supervise`, `child`, `grandchild`, `setsid-grandchild`, and `observer` are argv
modes. An additional `anchor` mode hosts crash experiments outside the supervised
unit; on Linux it is a subreaper. For `watch-parent`, `anchor` starts a separate
`crash-anchor` process that becomes the subreaper and waits for the supervisor,
the watcher's cause record, and the observer. This gives every profile the same
ancestry: entrypoint → intermediate subreaper → supervisor → watcher.
`-scenario` selects the consumer workload,
`-dir` selects markers, and `-gate` allows an external file to trigger stop.
Children accept `-term exit|ignore|record|trap`, `-delay`, `-grandchildren`, `-watch`, and
`-start-gate`. A start gate is released only after procguard assigns the unit.
The separate Windows regression deliberately omits that gate.

Stdout contains JSON objects, one per line, with a stable `event` key. Events
include `ready`, `started`, `after-start`, `exited`, `outcome`, `state`, and
`observed`. Exit and outcome records project cleanup confirmation as an explicit
`cleanup_confirmed` boolean (true only for the confirmed enum value); outcome
errors and exit errors are strings, never raw error objects. This is a consumer
projection, while the root process-integration tests exercise cleanup faults.
`supervise` owns SIGTERM handling and context cancellation. Successful scenarios,
including consumer cancellation after Docker stop, exit **0**; fixture/protocol
errors exit **2**. Workload children deliberately exit 1 or 42 to drive restarts.
The observer is a separate process outside the supervised unit, with its own
Unix session; it samples liveness for 750ms and reports zombies separately.

### Cgroup membership and removal

The `cgroup-v2` fixture keeps its child behind an exit gate. The parent waits for
the child's ready marker, reads its live `/proc/<pid>/cgroup` membership, then
releases the gate and waits for cleanup. Emergency cleanup releases the gate
before stopping the unit. Readiness, exit, and cleanup waits use bounded deadlines.
The fixture and harness share the leaf-removal assertion: only `fs.ErrNotExist`
confirms removal; an existing leaf or another stat error fails the scenario.

Run the deterministic gate and failure-injection regressions without delegation:

```sh
go test -tags e2e -count=1 -run TestCgroupOutcome ./e2e ./e2e/fixture
go test -count=1 -run TestLeafRemoved ./internal/e2eprotocol
```

For a required real-backend check, set `PROCGUARD_E2E_CGROUP_REQUIRE=1` and
`PROCGUARD_E2E_CGROUP_PARENT` to the absolute filesystem path of a writable,
process-free delegated cgroup parent with the memory controller enabled. Then run
`go test -tags e2e -count=1 -run 'TestNative/cgroup-v2' ./e2e`.
A missing or invalid delegation fails this required run rather than skipping it.

Docker's ordinary profiles deliberately provide no writable cgroup delegation.
Their `cgroup-v2` cases require a permission/read-only refusal before process
creation, with exactly one callback and PID zero. Positive coverage is mandatory
in `TestDelegatedCgroup`, not silently skipped when those profiles refuse it.

The positive test creates a temporary systemd user service with `Delegate=yes`
and `DelegateSubgroup=supervisor`, enables memory only in that owned unit, and
bind-mounts only its isolated test subtree writable into the container. It stops
the unit and removes containers and directories during teardown. Alternatively,
`PROCGUARD_E2E_CGROUP_PARENT` may name an existing delegated parent with the memory
controller enabled; the test creates and removes only its own child directory.
A missing user manager, controller, or delegation is a failure, not a skip.

This profile uses the host cgroup namespace so `/proc/<pid>/cgroup` paths match
the narrowly scoped bind mount. It drops all capabilities except `DAC_OVERRIDE`
for kernel migration permission checks and uses `no-new-privileges`. Its seccomp
filter is disabled because Docker's default filter blocks `clone3`; the other
profiles retain their usual filters. It never uses `--privileged` or a writable
host cgroup-root mount. CI starts a systemd user manager before the Docker suite;
local runs require an existing user manager or the explicit parent override.

```sh
PROCGUARD_E2E_REQUIRE=1 go test -tags e2e_docker -count=1 -run TestDelegatedCgroup -v ./e2e/docker
```

`TestCgroupNestedWorkerCleanupConfirmed` exercises natural leader exit with a
live setsid worker moved two cgroups below the launch leaf. It requires confirmed
cleanup, worker termination, and bottom-up subtree removal, both with atomic
`cgroup.kill` and with the pidfd fallback forced on. Run it under the same native
delegation as the other `TestCgroup` integration tests.

## Contracts and scenario IDs

Every [platform guarantee](platform-guarantees.md) row has a stable ID. The same ID
keys `e2e/expectations.go`. The harness checks table coverage in both runners;
a new row needs a scenario and all four platform expectations. Expectations are
explicit and never derived from procguard internals.

| ID                   | Linux                                                                            | Darwin                                                           | BSD native records                          | Windows                         |
| -------------------- | -------------------------------------------------------------------------------- | ---------------------------------------------------------------- | ------------------------------------------- | ------------------------------- |
| `tree-termination`   | Ignoring tree killed                                                             | Same                                                             | Same                                        | Job killed                      |
| `supervisor-death`   | Leader records SIGTERM and keeps running; all three survive                      | All three survive, no SIGTERM marker                             | Same as Darwin                              | Assigned tree dies              |
| `graceful-stop`      | Three cleanup markers, no kill                                                   | Same                                                             | Three markers, no kill                      | No polite signal; kill at grace |
| `natural-exit`       | Two descendant markers, no kill                                                  | Same                                                             | Two cleanup markers, no survivors           | Lingering job killed at grace   |
| `empty-unit`         | Prompt completion, no kill                                                       | Same                                                             | Same                                        | Same                            |
| `exit-killed`        | Hard kill recorded                                                               | Same                                                             | Same                                        | Same                            |
| `parent-limits`      | Observed OOM increase and exact clamped address-space limit                      | Rejected before launch                                           | Rejected before launch                      | Rejected before launch          |
| `self-address-space` | Observed exact clamped soft limit                                                | Observed exact target, or classified EINVAL with unchanged limit | Succeeds except OpenBSD reports unsupported | Unsupported                     |
| `self-oom`           | Observed OOM increase                                                            | Compatibility no-op                                              | Compatibility no-op                         | Compatibility no-op             |
| `watch-parent`       | Correct parent PIDs, adoption by intermediate subreaper                          | Correct changed parent PID                                       | Same as Darwin                              | Unsupported                     |
| `extra-files`        | Child reads descriptor 3 and acknowledges its payload                            | Same                                                             | Same                                        | Rejected                        |
| `stdin`              | Child echoes the full payload from standard input                                | Same                                                             | Same                                        | Same                            |
| `setsid-escape`      | Two live descendants each have their own SID and PGID after Stop                 | Same                                                             | Same                                        | Descendants die with job        |
| `start-failure`      | Three failed attempts, callbacks once, advancing failures, Stop works            | Same                                                             | Same                                        | Same                            |
| `repeated-launches`  | 50 launches; bounded fd/thread counts                                            | Explicit procfs-only skip                                        | Same                                        | Same                            |
| `pid1-reaping`       | Docker: six orphan zombies after three launches and two restarts; zero with init | Not applicable                                                   | Not applicable                              | Not applicable                  |

The BSD class needs one OS refinement for self limits: OpenBSD is explicitly
unsupported; FreeBSD, NetBSD, and DragonFly use the success expectation. Darwin
has its own class. Resource accounting compares a warmed baseline before launch
10 with the final state after launch 50: at most four additional descriptors and
eight additional runtime threads. This tolerates runtime scheduling while catching
per-launch retention of the spawning thread. It is not a general memory-leak test.

## Negative controls

A passing scenario is evidence only if disabling its mechanism makes it fail.
The recorded native Linux audit on 2026-09-23 used the controls below.
**FAIL -> FAIL** means the mutation was already caught and remains caught;
**PASS -> FAIL** identifies repaired coverage. Every unmutated native scenario
passed afterward. These historical results describe that audit's source snapshot,
not a current run or a mutation audit of other OS implementations.
Docker-only controls ran on 2026-09-23; each failed its scenario as intended,
and the mutated files were restored and compared byte-for-byte.

| Scenario             | Mechanism                                                                                          | Mutation in non-test code                                                                                                                        | How failure shows                                                                                                                                                                                                                       | Audit before → after                                            |
| -------------------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `supervisor-death`   | Linux launch-thread Pdeathsig delivers SIGTERM to the leader                                       | Delete `Pdeathsig: syscall.SIGTERM` in `procattr_linux.go`                                                                                       | Missing leader `.term` marker, even though all three processes survive. Darwin/BSD require zero markers; Windows requires zero survivors.                                                                                               | Native: PASS → FAIL                                             |
| `watch-parent`       | WatchParent cancellation preserves `*ParentExitError` and actual adoption PIDs                     | In `parent_linux.go`, replace both `cancelCause(&ParentExitError{...})` calls with `cancelCause(nil)`                                            | Wrong cause type and missing expected/observed PIDs. Expected PID must equal the killed supervisor; observed PID must equal the intermediate subreaper and must not be 1.                                                               | Native: FAIL → FAIL                                             |
| `tree-termination`   | Dedicated process group contains grandchildren for Stop                                            | Set `Setpgid: false` in `procattr_linux.go`                                                                                                      | Stop cannot finish within the deadline. Successful runs require exactly three observed PIDs, all dead.                                                                                                                                  | Native: FAIL → FAIL                                             |
| `graceful-stop`      | Group SIGTERM permits each handler to finish                                                       | Change `processTree.terminate` in `tree_unix.go` to send SIGKILL                                                                                 | Three per-PID SIGTERM cleanup markers are missing.                                                                                                                                                                                      | Native: FAIL → FAIL                                             |
| `natural-exit`       | Cleanup signals lingering descendants after natural leader exit                                    | At entry to `Child.finishTree`, return `(killed, nil)` when `deadline.IsZero()`                                                                  | Two descendant SIGTERM markers are missing; live descendants also violate the survivor assertion.                                                                                                                                       | Native: FAIL → FAIL                                             |
| `empty-unit`         | Wait detects an empty unit without spending the grace                                              | Insert `time.Sleep(c.grace)` at entry to `Child.finishTree`                                                                                      | Measured Wait completion exceeds the one-second bound with a ten-second grace.                                                                                                                                                          | Native: FAIL → FAIL                                             |
| `exit-killed`        | Exit reports hard escalation                                                                       | Set `Killed: false` in the final `Exit` literal in `child.go`                                                                                    | Exit.Killed differs from the explicit true expectation.                                                                                                                                                                                 | Native: FAIL → FAIL                                             |
| `parent-limits`      | Parent applies both child RLIMIT_AS and OOM adjustment                                             | Return nil immediately from `applyChildLimits`; also separately disable only its `if l.AddressSpace > 0` block                                   | `/proc/<pid>/limits` soft limit differs from `min(requested, hard)`, or observed OOM fails to increase to the requested value.                                                                                                          | Native: whole no-op FAIL → FAIL; address-space-only PASS → FAIL |
| `self-address-space` | ApplyLimitsToSelf requests RLIMIT_AS                                                               | Return nil immediately from `ApplyLimitsToSelf` in `limits.go`                                                                                   | Observed soft limit remains at its prior value instead of the exact clamped target. Darwin also permits only classified EINVAL with an observed unchanged limit, never an arbitrary error.                                              | Native: FAIL → FAIL                                             |
| `self-oom`           | ApplyLimitsToSelf writes oom_score_adj                                                             | Return nil immediately from `ApplyLimitsToSelf` in `limits.go`                                                                                   | Observed OOM score does not increase to its target.                                                                                                                                                                                     | Native: FAIL → FAIL                                             |
| `extra-files`        | ExtraFiles starts at descriptor 3                                                                  | Assign nil instead of `spec.ExtraFiles` to `cmd.ExtraFiles` in `Start`                                                                           | Child cannot read the known payload and replace it with the required acknowledgement.                                                                                                                                                   | Native: FAIL → FAIL                                             |
| `stdin`              | Stdin wires the child's descriptor 0                                                               | Assign nil instead of `spec.Stdin` to `cmd.Stdin` in `Start`                                                                                     | Child reads EOF immediately; the echoed payload is empty and the capability record reports a mismatch.                                                                                                                                  | Native: FAIL → FAIL                                             |
| `start-failure`      | AfterStart receives the error exactly once per failed attempt                                      | Duplicate `spec.AfterStart(pid, err)` in `Start`                                                                                                 | Callback count/attempt numbers differ from three callbacks for three failures; each must carry an error and PID zero.                                                                                                                   | Native: FAIL → FAIL                                             |
| `repeated-launches`  | Launches release descriptors                                                                       | Insert `_, _ = syscall.Open("/dev/null", syscall.O_RDONLY, 0)` in Linux `setProcAttr`, retaining the raw fd                                      | More than four additional descriptors between the warmed baseline and launch 50; baseline control grew by 41.                                                                                                                           | Native: FAIL → FAIL                                             |
| `setsid-escape`      | Group Stop leaves deliberate session escapes alive                                                 | Before group SIGKILL in `processTree.kill`, enumerate `/proc/*/stat` and SIGKILL processes whose PPID equals `t.pid`, including escaped children | Zero survivors instead of two. Independently, readiness records require each escape's SID and PGID to equal its own PID and differ from the leader's session/group.                                                                     | Native: FAIL → FAIL                                             |
| `pid1-reaping`       | procguard owns its leaders, not unrelated orphan reaping                                           | Replace Linux `processTree.close`'s no-op with a `unix.Wait4(-1, ..., unix.WNOHANG, nil)` drain loop                                             | In `pid1-noinit`, zombie counts stop accumulating as 2, 4, 6. Both profiles require three completed launches, two restarts, two backoff failures, nine distinct observed PIDs, and no live processes; init requires zero final zombies. | Docker: caught (2026-09-23 host run)                            |
| `docker-stop`        | Consumer cancellation reaches Child and finishes group cleanup                                     | Remove the `case <-ctx.Done()` arm from `Child.run`'s select                                                                                     | Docker reaches its five-second kill deadline, or final cancellation outcome / three cleanup markers / clean Exit is absent.                                                                                                             | Docker: caught (2026-09-23 host run)                            |
| `oom-decrease`       | Permission failure propagates and failed child is killed/reaped                                    | Make `writeOOM` return nil when `os.WriteFile` fails                                                                                             | Missing permission error or wrong callback/result; exactly one matching child PID must be observed dead and non-zombie.                                                                                                                 | Docker: caught (2026-09-23 host run)                            |
| `exhaustion`         | Real EAGAIN is reported through failed launches and callbacks; backoff advances and Stop completes | Suppress AfterStart on Start failure; independently make `Backoff.Next` keep its initial delay                                                   | Missing one-per-attempt callback or non-increasing delays. Exactly three failed attempts and callbacks must preserve EAGAIN, State must record no launches, and fillers must be dead and reaped.                                        | Docker: caught (2026-09-23 host run)                            |

For each native control, the audit used `cp` before editing, rebuilt the fixture
from the mutated library, and ran:

```sh
go test -tags e2e -count=1 -v \
  -run '^TestNative$/^SCENARIO$' ./e2e/
```

Replace `SCENARIO` with the table's ID. Restore the source from its backup using
`cp`, then require `cmp SOURCE BACKUP` to succeed before the next mutation or any
verification command. Every mutated source was restored byte-for-byte in this
audit. Do not supply `PROCGUARD_E2E_FIXTURE` during mutation runs: it would bypass
the rebuild. No mutation script is shipped because source-text recipes depend on
the exact implementation; apply one table mutation at a time.

For Docker controls, use `PROCGUARD_E2E_REQUIRE=1 go test -tags e2e_docker -count=1
-v -run 'TestProfiles/PROFILE/SCENARIO' ./e2e/docker` with `pid1-noinit`,
`pid1-init`, `nonroot`, or `pids` as appropriate: `pid1-noinit/pid1-reaping`,
both `pid1-noinit/docker-stop` and `pid1-init/docker-stop`,
`nonroot/oom-decrease`, and `pids/exhaustion`. The reaping mutation is caught by
the no-init profile; the init profile is the zero-zombie comparison.
Use the same backup/restore/cmp procedure. Do not interpret a build error, missing Docker, or skipped test as a
successful negative control.

Signal markers include their originating PIDs. Limit observations include the
previous, requested, and actual kernel values so an inherited limit cannot
silently make a no-op pass. A host already at the requested address-space limit,
or at maximum OOM adjustment, fails with an explicit diagnostic because no
observable change can be proved. Identity and observer records must describe the
same launched processes. The audit does not imply mutation coverage for every
branch of every platform implementation.

## Docker profiles

All profiles use a writable `/tmp` tmpfs and a read-only fixture mount. Containers
share the Docker host's Linux kernel; changing userland does not certify another
kernel or another OS.

| Profile            | Image / flags                                                                                        | Evidence and limits                                                                                                     |
| ------------------ | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `debian`           | `debian:bookworm-slim`, root                                                                         | Ordinary container lifecycle                                                                                            |
| `alpine`           | `alpine:3.21`                                                                                        | Static fixture in musl userland; does not test dynamic musl linking                                                     |
| `nonroot`          | Debian, `--user 65534:65534`                                                                         | Lifecycle, OOM increase, forbidden decrease below inherited value; failed child is gone and callback receives the error |
| `restricted`       | Debian, `--cap-drop ALL --security-opt no-new-privileges --read-only`                                | Lifecycle without capabilities and with all writable paths under `/tmp`; not every possible seccomp policy              |
| `pids`             | Debian, `--pids-limit 64`, `GOMAXPROCS=2`                                                            | Cgroup task exhaustion, failed Start, callback accounting, increasing backoff, Stop, and filler cleanup                 |
| `pid1-noinit`      | Debian, fixture supervisor as entrypoint                                                             | PID 1 signal handling and orphan-zombie accumulation across three launches and two restarts                             |
| `pid1-init`        | Same with `--init`                                                                                   | Init reaps orphans; consumer still handles stop                                                                         |
| `delegated-cgroup` | Debian; owned writable cgroup subtree, host cgroup namespace, only `DAC_OVERRIDE`, no seccomp filter | Required clone-time placement, memory limit setup, confirmed cleanup, and leaf removal                                  |
| `hidepid`          | Skipped                                                                                              | A private proc mount with hidepid needs privileges; permission failures instead have deterministic unit coverage        |
| `qemu-arm64`       | Alpine, `--platform linux/arm64`                                                                     | Opt-in graceful/hard/natural-exit smoke subset                                                                          |
| `qemu-riscv64`     | `debian:trixie-slim`, `--platform linux/riscv64`                                                     | Same smoke subset; newer Debian userland accommodates riscv64                                                           |

The pids budget counts **tasks**, including Go threads: allow about 8 threads for
the supervisor and 8 each for a leader plus two grandchildren (32 total), plus
up to 8 for the observer (40). A limit of 64 leaves 24 tasks of headroom for
runtime activity and CLI exec. The exhaustion workload warms the spawning thread,
then fills remaining slots with single-threaded `/bin/sleep` processes until
fork reports EAGAIN. Three real launch failures use 1s, 2s, and 4s backoff
observations; Stop interrupts the last wait. Fillers are killed and reaped before
the independent observer runs. Runtime thread counts vary, so the test measures
the actual limit by failed fork instead of assuming a fixed number of fillers.

Crash tests run the supervisor beneath `anchor` and use `docker exec kill -9`
from outside its unit. Killing container PID 1 tears down the namespace and
cannot measure Linux's weaker arbitrary-child parent-death guarantee. The two
PID 1 stop tests instead use `docker stop --time 5`: SIGTERM cancels the consumer,
the tree finishes gracefully, and the container must exit 0 within five seconds.
Init is tested only for orphan reaping, not as a procguard feature.

For `watch-parent`, only the kill command uses `docker exec`; the watcher and
its ancestors are descendants of the `docker run` entrypoint. Without the
intermediate `crash-anchor`, that entrypoint would itself be PID 1 in every
profile except `pid1-init`, so adoption would report 1 even with subreaper setup
succeeding. With `--init`, Docker's init occupies PID 1. The separate intermediate
now proves subreaper adoption in every tested profile: the observed parent must
equal that anchor's PID and must not be 1. Subreaper setup errors fail the fixture.

The reaping workload exits 1, 1, then 42. Each of the first two exits schedules
a restart and advances backoff. Exit 42 is prevented from restarting, so it
neither advances nor resets backoff: final State requires `Launches=3`,
`Restarts=2`, and `Failures=2`. The fixture does not perform unrelated orphan
reaping in this scenario.

`task e2e:binfmt` prints the following command and explains it; it never runs it:

```sh
docker run --platform linux/amd64 --privileged tonistiigi/binfmt:qemu-v9.2.2 --install arm64,riscv64,arm
```

An owner may run it after reviewing the host-wide binary interpreter change.
`PROCGUARD_E2E_QEMU=1` only enables tests; it does not register binfmt. Qemu-user
still uses the host kernel and is a smoke test, not architecture certification.

## Continuous integration

`.github/workflows/ci.yml` runs build/vet checks for Linux, macOS, FreeBSD,
OpenBSD, NetBSD, and Windows on amd64 and arm64, plus DragonFly on amd64. Native
vet, unit, and race tests run on Linux, macOS, and Windows. Both CI and
`.github/workflows/e2e.yml` run on
pull requests, pushes to `main`, and manual dispatch. E2E has no repository-variable
enablement gate. Native E2E jobs cover Ubuntu amd64/arm64, macOS arm64, and Windows.
Docker profiles set `PROCGUARD_E2E_REQUIRE=1`, so unavailable Docker fails the job.
Optional qemu-user profiles and host binfmt registration are not automatic.

BSD E2E uses `cross-platform-actions/action` v1.7.0, pinned to
`82668f1fbd89d9c99487604f822cbf6716f124d3`: FreeBSD 15.1 and NetBSD 11.0 run
on amd64/arm64, OpenBSD 7.9 on amd64, and OpenBSD 8.0 on arm64.
The host builds static root-test, E2E-test, and fixture binaries for the guest architecture.
Each guest compiles `internal/bsdabi/generate.c` with `-Wall -Wextra -Werror` and compares its native
ABI output with the checked-in target file before running the root and E2E tests.
The guest executes the binaries through `sh` and needs no Go installation.
`PROCGUARD_E2E_FIXTURE` supplies the prebuilt fixture; `PROCGUARD_E2E_ROOT`
locates the checkout for contract coverage. Root and E2E output is retrieved even
after failure and uploaded separately. The matrix defines tested configurations;
it does not imply every current run passes.

DragonFly has no hosted guest job. `scripts/dragonflyvm.sh up|test|down` uses the
SHA-256-pinned DragonFly 6.4.2 image in a local KVM-backed Docker container. Its
`test` command builds static root and E2E binaries, verifies the native generated
ABI, runs both suites in the guest, and preserves logs under the configured VM
cache directory.

### Documentation website

The Pages workflow builds the documentation with `task docs:build` in strict
mode and deploys `site/` to GitHub Pages at
[procguard.ovineko.com](https://procguard.ovineko.com). Automatic deployment is
limited to stable `vX.Y.Z` tag pushes; pushes to `main` and prerelease tags do
not deploy. Manual dispatch builds the selected ref. Deployment uses the
`github-pages` environment and its ref restrictions; the custom domain is
configured through Pages settings and DNS, not a generated `CNAME` file.

## Portable binaries and cross-architecture execution

Unit helpers re-execute the absolute test-binary path. They never invoke Go at
runtime. Native e2e accepts `PROCGUARD_E2E_FIXTURE=/path/to/fixture`; set
`PROCGUARD_E2E_ROOT` to a directory containing `docs/guides/platform-guarantees.md`.
The cross-architecture bundle includes that table and the unit fuzz seeds, so targets need no checkout
or Go toolchain.

```sh
sh scripts/crossarch.sh docker
# Equivalent: task test:crossarch
```

The script builds static unit and native-e2e test binaries and the fixture for
linux/amd64, arm64, riscv64, arm/v7, and 386. Each Docker run uses an
architecture-specific Debian image digest, `--platform`, and `--init`, and
reports PASS or FAIL per architecture. A failure does not suppress later
architectures. The exit status is nonzero if any architecture fails. The Docker
daemon must be local for bind mounts. Foreign architectures require working
binfmt interpreters on the Docker host. The script never registers them;
`task e2e:binfmt` prints the manually operated setup command.

The runner declares `PROCGUARD_TEST_EMULATED=1` for foreign architectures (qemu-user presents a
guest-looking `/proc/cpuinfo`, so auto-detection is only a fallback) and skips the `RLIMIT_AS`
checks there with that reason. The runner sets `PROCGUARD_TEST_TIME_SCALE=8` for foreign architectures and 1 for
native execution (including 386 on amd64). This positive integer defaults to 1.
Use 5–10 on slow boards. Readiness deadlines, cleanup budgets, observation windows,
and elapsed-time assertions use `internal/testsupport.Scale`; backoff arithmetic
and API validation inputs retain their specified values. Polling intervals are
sampling frequencies, not deadlines. Intentional cleanup work and model jitter
also scale. Invalid settings or duration overflow fail explicitly.

Linux address-space tests compare the executable's ELF machine family with the
host `/proc/cpuinfo` and check executable qemu mappings in `/proc/self/maps`.
Native compatibility execution (386 on amd64 or arm on arm64) is not classified
as emulation. The detector has synthetic native, compatibility, and emulated
cases. Missing procfs inputs fail detection; unfamiliar CPU families alone are
not evidence of emulation. The check targets Linux qemu-user, not full-system
emulation or arbitrary binary translators. Under detected emulation, unit and
native-e2e `RLIMIT_AS` checks explicitly SKIP: the emulator's own reserved address
space invalidates that experiment. OOM adjustment tests continue to run.
`TestWatchParentRealSignal` explicitly skips with no visible parent (PID 1 with
PPID 0); `--init` lets it execute normally.

For a board without Go or Docker:

```sh
sh scripts/crossarch.sh pack /tmp/procguard-tests.tar.gz
scp /tmp/procguard-tests.tar.gz board:/tmp/
ssh board 'mkdir -p /tmp/procguard-tests && tar -xzf /tmp/procguard-tests.tar.gz -C /tmp/procguard-tests && PROCGUARD_TEST_TIME_SCALE=8 /tmp/procguard-tests/run.sh riscv64'
```

`board` is an SSH configuration alias. The runner auto-detects the native
architecture if its argument is omitted. It runs both suites even if one fails.
The Go test process-wide watchdog is disabled in the bundle; individual tests
retain scaled operation deadlines. CI or an SSH caller can impose an additional
outer timeout.

## Fuzzing and Supervisor model

```sh
task fuzz FUZZTIME=60s
# Without Task:
sh scripts/fuzz.sh 60s
PROCGUARD_MODEL_RUNS=1000 go test -race -run Model ./...
# Replay one schedule:
go test -race -run '^TestSupervisorModel$/^seed=42$' -seed=42 .
```

The fuzz task runs each target separately with two workers by default (override
`GOMAXPROCS`), using seed files in `testdata/fuzz/`. Targets cover proc-stat
parsing and generated round trips, Limits, Spec, Options, WatchParent validation,
Backoff transitions, and the actual e2e JSON-line decoder. Valid Options buffers
are bounded to 1023 entries to avoid testing allocator exhaustion. Validation
fuzzing never applies process limits to the test runner. The proc-stat target
runs only on Linux; other targets are portable.

The Supervisor model runs 200 schedules by default, starting at `-seed=1`.
Each schedule selects a restart policy, a SIGTERM behavior, and a sequence of
explicit restarts, MarkStable, Signal, self-inflicted signal crashes,
successful/failing/prevented exits, Stop,
and context cancellation, with timing jitter. Real re-executed children announce
readiness and accept exit commands through temporary files. A sequential reference
model checks each relaunch decision, exact launch/restart/failure counters,
backoff delays, terminal reasons, and event order. An independent PID probe checks
that leaders are dead after exit and Run; State is checked after leader exit
observation, avoiding a race between a live snapshot and a later OS probe.
Failure output includes the seed and action trace.
The model requires `CleanupConfirmed` for every observed exit and the terminal
outcome, independently of diagnostics in `Exit.Err`. It logs those diagnostics
without discarding them; PID liveness, event order, status, and restart policy
remain separately checked.

Self-inflicted crash helpers wait on their existing deadline after a successful
kill request: on macOS the syscall can return before signal delivery, so returning
an ordinary exit code would race the crash being tested. Writer-error helpers
emit their stdout payload in one write; on BSD a second write can receive SIGPIPE
after the parent's failing writer closes the output pipe.

Windows descendant regressions verify membership in the assigned Job Object
before natural leader exit. The normal and race suites then require graceful or
immediate cleanup of that descendant without an output-drain timeout. Job Object
calls preserve compiler pointer tracking by converting ABI buffers only in direct
native syscall arguments, avoiding stale stack addresses and per-probe allocation.

The package `TestMain` snapshots all goroutine stacks before tests and allows a
bounded cleanup window afterward. New goroutines with procguard frames fail with
their complete stacks. Model schedules also check independently after cleanup.
Helper subprocesses exit deliberately and do not run this check. The detector
checks retained goroutines, not a fixed total count of runtime workers.

Hardening regressions include malformed proc-stat prefixes, a fuzz-discovered
vertical-tab separator accepted as a kernel field boundary, and the scheduling
window where `State.Running` remained true after `Child.Exited()` closed.
`TestParseProcessStatMalformedPrefix` and `TestStateObservesClosedLeaderExit`
cover these deterministically; the latter was exposed by the model under `-race`.
`TestHelperRelativeInvocationWithWorkingDirectory` covers re-execution from a
relative invocation while the child uses a different working directory.

The minimized parser failure is retained as
`testdata/fuzz/FuzzParseProcStat/7b3ce998dae9aa0b` (`1 () X 0\v0`).
The parser requires literal spaces around its state/parent/group fields and
signed 32-bit PID values on every architecture. The corpus is replayed by
ordinary `go test` as well as fuzz runs.

## Mutation testing

```sh
task mutation
# Without Task (gremlins must be on PATH):
gremlins unleash . --output coverage/mutation.json
```

Create `coverage/` first when invoking gremlins directly. `.gremlins.yaml` limits
execution to two workers and uses a timeout coefficient of 10. Gremlins derives
the timeout from its initial coverage run (about nine seconds on the measured
Linux amd64 host), allowing about 90 seconds per mutant. Its default mutation
operators remain enabled. The file exclusion matches subdirectories because
`unleash .` otherwise walks the entire module, including fixture consumers.
Platform files remain in the report, even when excluded by the current Go build.
The recorded 2026-09-24 audit used `--workers 4`; the task keeps the more conservative default
of two. An instrumented limit helper records the requested kernel value and
restores its original soft limit before coverage flushing can allocate memory.

Triage every lived and not-covered mutant. Add a behavioral regression for a
real gap; explain equivalent mutations algebraically; retain defensive branches
with an explanation of the unavailable failure or platform. Never suppress a
survivor merely to improve the score. Timeouts are reported separately, not
counted as kills. Global constant expressions have no Go statement-coverage
locations, so gremlins labels them not covered; exercise those mutations
separately with Go source overlays. This leaves original sources untouched.

The cleanup loop accepts a private interface for group probes and signals.
Deterministic tests verify that successful hard kills are followed by an empty
group observation, probe failures remain errors, and kill errors are preserved.
Native process tests continue to exercise the actual platform implementation.

### Mutation audit (Linux amd64)

Historical measurements recorded on 2026-09-24, using a development build of
gremlins and Go 1.26.5 (the exact gremlins build was not recorded), are retained
below. They are not a current score or a fully pinned
benchmark; rerun the command above for the current source tree.

| Root-package result       |     Before |      After |
| ------------------------- | ---------: | ---------: |
| Killed                    |        139 |        155 |
| Lived                     |         25 |          9 |
| Not covered               |         37 |         37 |
| Timed out                 |          3 |          3 |
| Killed / (killed + lived) | **84.76%** | **94.51%** |

An intermediate run reached 90.85% (149 killed, 15 lived). All 16 identified gap
mutants were killed in the final full root scan. The nine remaining survivors are
five equivalent cases and four defensive/unreachable cases. The final run took
3m36s with four workers. Timeout, not-covered, and equivalent counts remain
visible; none are relabeled as kills to improve the score.

Positions below identify the 2026-09-24 audited source snapshot, not stable
line references into later revisions. The three extracted cleanup checks were
previously at `child.go:263`, `:278`, and `:284`. The initial unrestricted scan
also visited subpackages; the comparison filters that scan to root files,
matching the checked-in configuration. It does not silently remove platform
files or equivalent survivors from the denominator.

| Initial survivor                                                       | Triage                                         | Evidence or regression                                                                                                                                                                                                       |
| ---------------------------------------------------------------------- | ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `backoff.go:66:20` boundary                                            | Equivalent                                     | At zero failures the delay is zero and maximum is positive, so changing `> 0` to `>= 0` cannot make Saturated true.                                                                                                          |
| `backoff.go:72:15`, `:79:11`, `:90:11` boundaries                      | Gap, killed                                    | `TestBackoffZeroValueContract` checks zero-value stability and the complete default delay sequence.                                                                                                                          |
| `backoff.go:94:38` boundary                                            | Equivalent                                     | At `delay == maximum`, an extra loop iteration returns that same maximum.                                                                                                                                                    |
| `backoff.go:95:12` boundary                                            | Gap, killed                                    | `TestBackoffOddMaximum` uses `2s + 1ns`; integer division must return 2s before reaching the cap.                                                                                                                            |
| `child.go:92:24` arithmetic                                            | Gap, killed                                    | `TestStartDefaultGraceAndDrainAllowance` checks the default 10s grace plus 2s output-drain allowance.                                                                                                                        |
| `child.go:170:22` boundary                                             | Gap, killed                                    | The same test launches with zero grace, which must select the default.                                                                                                                                                       |
| `child.go:173:40` negation                                             | Gap, killed                                    | `TestStartRejectsNegativeLimitBeforeSpawn` requires validation failure, no PID, and one callback.                                                                                                                            |
| `child.go:219:10` negative inversion and arithmetic (2 mutants)        | Defensive/unreachable with supported ownership | The fallback exit code is overwritten after successful reaping. A missing ProcessState needs a wait failure such as an incompatible external reaper; that ownership violation is outside the contract.                       |
| `child.go:273:14` negation                                             | Gap, killed                                    | `TestCleanupFailuresAndKillObservation/initial-probe-error` rejects swallowing a probe failure as an empty group.                                                                                                            |
| `child.go:288:15` negation                                             | Gap, killed                                    | Scripted cleanup tests require both propagation of a kill error and observation of an empty group after a successful kill.                                                                                                   |
| `child.go:294:29` negation                                             | Gap, killed                                    | Scripted cleanup tests require errors after failed probes and continued observation after successful probes.                                                                                                                 |
| `limits.go:27:49` negation                                             | Defensive error propagation                    | Requires a current-process RLIMIT_AS syscall failure after valid public input; Linux happy-path self-limit tests cannot produce this reliably.                                                                               |
| `parent.go:36:52` boundary                                             | Gap, killed                                    | `TestWatchParentIntervalEndpoints` accepts exactly 60s as well as 500ms.                                                                                                                                                     |
| `rlimit_as.go:13:11` boundary                                          | Unreachable through public API                 | ApplyLimitsToSelf skips the syscall helper for zero bytes; changing the helper's zero handling is not observable through it.                                                                                                 |
| `rlimit_as.go:40:11` boundary                                          | Equivalent on Linux                            | Zero converts to zero and clamps to zero with the unsigned Linux hard-limit type.                                                                                                                                            |
| `rlimit_as.go:44:9` boundary                                           | Equivalent                                     | When request equals hard limit, either return path returns the same value.                                                                                                                                                   |
| `supervisor.go:388:24` increment/decrement                             | Gap, killed                                    | `TestDroppedEventsExactCount` requires exactly two dropped events when a one-slot queue retains only EventStarted.                                                                                                           |
| `tree_members_linux.go:59:29` arithmetic                               | Equivalent rejection                           | A closing parenthesis before the opening delimiter necessarily leaves a nonnumeric PID prefix, rejected by the next parse. Otherwise the closing parenthesis is already at least `start+2`. Error wording is not a contract. |
| `tree_members_linux.go:63:42`, `:63:59`, `:74:74`, `:81:53` boundaries | Gaps, killed                                   | `TestProcessStatNumericEndpoints` accepts leading-zero positive PIDs and digit 9 at each numeric field's boundary.                                                                                                           |

All 37 not-covered root mutants are accounted for below. Non-native rows are
unreachable in this Linux build, not evidence that the corresponding platform
has passed a mutation audit. Existing platform tests and the VM runner provide
execution paths; repeat mutation testing on those hosts before claiming a score.

| Not-covered positions                                                                                        | Count | Triage                                                                                                                                                                                                                                       |
| ------------------------------------------------------------------------------------------------------------ | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `backoff.go:9:27`, `:10:26`, `:11:27`, `:13:26`, `:14:27`, `:15:26`, `:16:28`, `:17:27` arithmetic           | 8     | Coverage-mapping limitation for constants. All eight multiplication-to-division mutants were manually killed using source overlays and the default-contract, WatchParent, and descendant-escalation tests. Kept outside the automated score. |
| `limits_other.go:11:30`, `:11:62`, `:14:18`, `:14:55` (boundary and negation)                                | 5     | Non-Linux capability validation; not compiled on this host.                                                                                                                                                                                  |
| `parent_unix.go:17:37`, `:29:40`                                                                             | 2     | Non-Linux Unix polling implementation; not compiled on this host.                                                                                                                                                                            |
| `rlimit_unsupported.go:8:11` (boundary and negation)                                                         | 2     | Windows/OpenBSD unsupported address-space helper; not compiled on this host.                                                                                                                                                                 |
| `tree_bsd.go:19:9`, `:27:9`, `:35:37`, `:51:10`, `:54:8` (boundary and negation)                             | 6     | BSD kqueue observation; not compiled on this host.                                                                                                                                                                                           |
| `tree_members_darwin.go:16:9`, `:20:30`, `:20:66`, `:20:98`                                                  | 4     | Darwin group enumeration; not compiled on this host.                                                                                                                                                                                         |
| `tree_windows.go:20:9`, `:26:9`, `:32:9`, `:36:63`, `:45:89`, `:52:54`, `:60:9`, `:68:32`, `:86:9`, `:89:30` | 10    | Windows job setup, cleanup, and queries; not compiled on this host.                                                                                                                                                                          |

The three timeout mutations negate the launch-thread handshakes at
`spawn_linux.go:19:10` and `:25:25`, and the child-presence check at
`supervisor.go:210:13`. They deadlock or strand lifecycle work. They remain
separate timeout results even with the generous per-mutant deadline.

## Merged coverage

```sh
task coverage
# Without Task:
sh scripts/coverage.sh
```

The measurement includes the root library and `internal/...`. Runnable examples,
the e2e harness, and fixture command implementations are consumers and are outside
the regression denominator. They still execute to exercise the library. The
fixture main package must be instrumented along with the library so normal exits
flush counters; processes killed by signals cannot flush their final counters.

The script writes `coverage/unit.out` with `go test -coverprofile`. A temporary
`-exec` wrapper restores the unit counter directory after Go sets up the test
environment, so re-executed helper processes contribute too. It builds the
fixture using `go build -cover`, and runs a compiled native e2e test binary with
`GOCOVERDIR`. Running the binary directly prevents `go test` from replacing that
variable in the fixture's environment. Separate directories hold unit, harness,
and fixture counters; missing fixture counters fail the run. `go tool covdata
merge` combines counters and `textfmt` produces `coverage/merged.out` without
counting a source block twice. `coverage/e2e.out` preserves the separate native
e2e profile. `coverage/functions.txt` and `coverage/files.txt`
contain function and statement-weighted per-file results. Temporary counters
are removed on exit; Go cache locations are inherited from the caller.

`scripts/coverage-threshold.txt` stores the merged coverage floor as a percentage.
The gate compares the unrounded statement ratio with that floor and exits nonzero
on a regression. The baseline is native Linux amd64; other platforms have a
different compiled source set and need their own measurement before changing the
floor. Keep the floor within one percentage point of the accepted baseline.

### Coverage audit (Linux amd64)

The historical 2026-09-24 measurement has a merged baseline of
**95.23% (599/629 statements)**. The checked-in floor is **94.23%**.
Unit coverage was **94.91% (597/629)** and native E2E alone was
**68.68% (432/629)**. The root library alone was **96.24% (486/505)** in both
unit and merged profiles; E2E added two covered statements in the internal
protocol package and independently exercised the public lifecycle contract.
These counts describe that source snapshot, not a current execution result.

| File                                      | Merged statements |
| ----------------------------------------- | ----------------: |
| `backoff.go`                              |   100.00% (40/40) |
| `child.go`                                |  95.62% (131/137) |
| `internal/e2eprotocol/decode.go`          |   100.00% (17/17) |
| `internal/e2eprotocol/protocol.go`        |    88.89% (16/18) |
| `internal/testsupport/config.go`          |    94.44% (17/18) |
| `internal/testsupport/emulation.go`       |    93.10% (27/29) |
| `internal/testsupport/emulation_linux.go` |    73.33% (11/15) |
| `internal/testsupport/leaks.go`           |    92.59% (25/27) |
| `limits.go`                               |    90.91% (10/11) |
| `limits_linux.go`                         |    86.67% (13/15) |
| `parent.go`                               |     100.00% (8/8) |
| `parent_linux.go`                         |   100.00% (28/28) |
| `procattr_linux.go`                       |     100.00% (1/1) |
| `rlimit_as.go`                            |    81.25% (13/16) |
| `spawn_linux.go`                          |   100.00% (16/16) |
| `supervisor.go`                           |  98.81% (166/168) |
| `tree_linux.go`                           |     80.00% (8/10) |
| `tree_members_linux.go`                   |   100.00% (44/44) |
| `tree_unix.go`                            |     72.73% (8/11) |

Every measured non-platform file is above 80%. The two platform files below 80%
retain specific defensive paths: `emulation_linux.go` handles missing/unreadable
procfs or executable metadata, and `tree_unix.go` handles signal permission
errors and unattached-child cleanup (Linux tree creation itself cannot fail).
Runnable examples are excluded consumers rather than artificially tested for
coverage. The gate was checked with an exact-threshold profile, a below-threshold
profile, and a zero-coverage profile; only the first passes.

## FreeBSD VM

```sh
task test:freebsd
# Or operate the persistent VM explicitly:
sh scripts/bsdvm.sh up
sh scripts/bsdvm.sh test
sh scripts/bsdvm.sh down
```

The host needs amd64 Linux, Docker, accessible `/dev/kvm`, `curl`, `sha256sum`,
`xz`, OpenSSH, and `tar`. Ports are fixed to localhost:2222. Network access is
needed for the release image, container packages, guest packages, and Go modules.
`PROCGUARD_VM_DIR` defaults to `$HOME/.cache/procguard-vm`; use the same directory
for every command. A cache lock rejects concurrent operations. After an
uncatchable termination, remove the stale `lock` directory only after checking
that no operation is running.

`up` downloads the official FreeBSD 14.5 RELEASE amd64 BASIC-CLOUDINIT UFS qcow2
image and its release `CHECKSUM.SHA256` manifest over HTTPS, requires exactly one
matching SHA-256, and verifies the compressed image before decompression. The
cache contains an immutable base and a persistent writable qcow2 overlay.
A dedicated Ed25519 SSH key and NoCloud `cidata` ISO authorize the `freebsd` user.
The seed is built with xorriso in an Alpine container. Every container run
specifies `--platform linux/amd64`, including seed creation. QEMU uses KVM,
`-cpu host`, four CPUs, 4096 MiB RAM, virtio disk/network, and user networking.
The Alpine image is pinned by digest. SSH readiness is bounded; on failure the
runner prints the container's last logs. The dedicated known-hosts file accepts
the first key and rejects changed keys.

`test` builds static FreeBSD/amd64 root-test, E2E-test, and fixture binaries on
the host, transfers them with the current sources and guarantee table, and
performs a native `cc -Wall -Wextra -Werror` ABI generation/comparison in the
guest. It runs the root binary, all Go unit tests, `-race -count=3`, the prebuilt
native E2E binary, and 1000 model schedules under the race detector. Guest output
and test binaries are removed on exit. `down` stops and removes the named
`procguard-freebsd` container while preserving cached disks and keys. Use `down`
before changing cache directories; starting from scratch requires deliberately
removing the cache, including its known-hosts file.

`scripts/dragonflyvm.sh up|test|down` provides a separate local DragonFly 6.4.2
amd64 guest. It verifies the released qcow2 image against SHA-256 before use,
creates a writable overlay, and runs QEMU in an Alpine container pinned by digest.
`test` transfers static root/E2E binaries, the fixture, native ABI generator, and
guarantee table; it compiles and compares the generated DragonFly ABI before
running both suites. Root and E2E logs remain under
`$PROCGUARD_VM_DIR/dragonfly/artifacts/logs`. This guest uses the builder test
image's `runner` root password; never use that image or credential for production.
The host needs Docker and accessible `/dev/kvm`, and must be amd64 Linux.

The BSD workflow matrix runs native root/E2E suites and ABI comparisons for
FreeBSD 15.1 and NetBSD 11.0 on amd64 and arm64, plus OpenBSD 7.9 amd64 and
OpenBSD 8.0 arm64. The OpenBSD ARM guest uses the action's `virt,highmem=off,acpi=off`
machine profile with 3072 MiB. DragonFly runs through the explicit local guest
runner rather than a hosted CI guest. Native root/E2E and ABI generation evidence
has been collected on FreeBSD 14.5 amd64 and 15.1 arm64, OpenBSD 7.9 amd64 and
8.0 arm64, NetBSD 11.0 amd64/arm64, and DragonFly 6.4-RELEASE amd64.
