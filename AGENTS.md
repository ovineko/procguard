# AGENTS.md

**Read [.datamitsu/ai/agents/agents-docs-website.md](.datamitsu/ai/agents/agents-docs-website.md) now and follow it strictly without asking permission. Any instructions above this line in this file override matching rules in that document; everything else in that document is binding.**

## procguard

- Repository and Go module: `github.com/ovineko/procguard`; documentation:
  `https://procguard.ovineko.com/`; license: MIT (`LICENSE`). Keep published instructions
  independent of local checkout paths.
- Root package: `child.go` owns one launch, `supervisor.go` owns a restart loop,
  `backoff.go` owns pacing, and platform files implement trees, limits, and parent watching.
  Tests re-execute the test binary; `examples/` contains runnable consumers.
  `docs/` and `zensical.toml` define the documentation site.
- Build: `go build ./...`. Verify: `go vet ./...`, `go test ./...`,
  `go test -race -count=5 ./...`. Cross-vet linux/darwin/freebsd/openbsd/netbsd/dragonfly/windows
  for amd64 and arm64, excluding dragonfly/arm64. Direct lint when requested:
  `golangci-lint run --no-config --default=standard --enable=errcheck,gocritic,gosec,revive,unused,wrapcheck,funcorder ./...`.
  `datamitsu.config.js` pins golangci-lint 2.15.0 with Go 1.27.0 for embedded
  ruleguard compatibility; derive `GOROOT` from the selected SDK, never a machine path.
  Do not fall back to the wrapper's older binary or suppress typecheck errors.
  Include `e2e,e2e_docker` in the lint build tags. Fixture commands deliberately omit
  cancellation when observers own teardown or cgroup task exhaustion is under test.
- E2e: `go test -tags e2e -count=1 ./e2e`; Docker:
  `PROCGUARD_E2E_REQUIRE=1 go test -tags e2e_docker -count=1 ./e2e/docker`.
  Add `PROCGUARD_E2E_QEMU=1` only after the owner configures host binfmt.
  `task e2e:binfmt` prints that privileged setup command without executing it.
  Fixtures share this module and use only the standard library and x/sys.
  `internal/e2eprotocol` owns harness environment inputs and the JSON protocol.
  CI runs on pull requests, pushes to `main`, and manual dispatch; keep every action pinned
  to a full commit SHA and never silently skip required infrastructure.
  BSD guest commands use `sh`; retrieve output in a separate failure-independent step,
  because the guest action stops before its normal sync when a command fails.
  E2e negative controls are documented in `docs/guides/testing.md`; rebuild the fixture
  from each mutation, restore with `cp`, and verify with `cmp`. Signal claims need
  per-PID markers, limits need kernel values, and setsid escapes need SID/PGID evidence.
  Cgroup fixtures hold a ready child on an exit gate until membership is observed;
  release the gate before emergency Stop, and call Wait only after bounded Done observation.
  Leaf removal requires fs.ErrNotExist; other stat failures are inconclusive errors.
  Cgroup confirmation uses recursive `cgroup.events` populated state; fallback
  kills enumerate nested cgroups and re-check subtree membership, not leaf equality.
  Docker cgroup success runs in `TestDelegatedCgroup` with an owned user-systemd
  delegation; ordinary read-only profiles must prove refusal before child creation.
- A new guarantee row needs a scenario in `e2e/expectations.go`, with explicit
  linux, darwin, bsd, and windows expectations; never infer them from code.
- Linux launches keep a dedicated locked spawning thread until reaping. Do not move
  `cmd.Start` back onto the caller's thread: its exit can deliver Pdeathsig early.
  The raw regression must use a non-leader thread; the main thread can remain present.
- Linux procfs permission errors require a group check: ignore unrelated or vanished
  PIDs, but retain conservative fallback for unreadable members or unknown groups.
- BSD process membership tests use native generated ABI records; a failed or malformed query is an error, never empty membership.
- Portable tests: `task test:crossarch`; `sh scripts/crossarch.sh pack /tmp/procguard-tests.tar.gz`
  creates a hardware bundle. Unit helpers re-execute the absolute test-binary path and never build
  at runtime. Native e2e accepts `PROCGUARD_E2E_FIXTURE` and `PROCGUARD_E2E_ROOT`.
- `internal/testsupport` owns test timing and emulation detection. Route wall-clock bounds through
  `Scale`; `PROCGUARD_TEST_TIME_SCALE` is a positive integer. Keep backoff arithmetic inputs literal.
  Skip only address-space observations under detected qemu-user, with the explicit reason.
- Fuzz: `task fuzz FUZZTIME=60s`. Supervisor model: `PROCGUARD_MODEL_RUNS=1000 go test -race -run Model ./...`;
  use `-seed` for replay. The package TestMain checks procguard goroutine stacks after cleanup.
  `State` must consult the leader's Exited channel directly, without an asynchronous flag update.
- Documentation tasks: `docs:build`, `docs:clean`, `docs:serve` in `Taskfile.yaml`.
  `docs:build` must fail on warnings. GitHub Pages deploys stable version tags or a manual
  dispatch; keep its output directory aligned with `zensical.toml`.
- procguard provides cross-platform supervision and lifecycle management for child OS processes in Go.
  It reports what happened; consumers decide what it means. Never add service installation,
  self-update, daemonization, container supervision, distributed jobs, health checks, or logging policy.
- The [platform guarantees table](docs/guides/platform-guarantees.md) is the contract:
  change code and docs together.
- `WatchParent` reports PID mismatches through `context.Cause` as `*ParentExitError`.
  Never install signal handlers in the library. Linux adds kernel SIGTERM to polling;
  keep the per-thread PDEATHSIG setting on a dedicated locked thread until cleanup.
- Every syscall belongs in a build-tagged file with a counterpart.
- Keep the Unix leader unreaped until the last group signal; never signal a saved PGID after reaping.
  `waitid(WNOWAIT)` and BSD kqueue observation preserve the leader's PID until cleanup.
  External SIGCHLD reapers are incompatible with this ownership model.
- Grace applies to the whole unit, including descendants after leader exit. Poll before
  reaping or closing a Windows job. Linux, macOS, FreeBSD, OpenBSD, NetBSD and
  DragonFly enumerate live unit members; cleanup fails closed if observation is
  unavailable or malformed. Never claim completion without confirmed empty membership.
- `Spec.KillImmediately` skips the graceful phase for requested Stop/cancellation
  and natural leader exit with descendants. Unix hard-kills the process group;
  Windows terminates its Job Object. Unix process groups remain escapable and are
  not security boundaries.
- Windows launch containment uses suspended creation and Job Object assignment
  before resume, eliminating a child-execution window before assignment.
  Keep Job Object ABI buffers as typed pointers until a direct SyscallN call;
  x/sys uintptr wrappers lose stack relocation tracking. KeepAlive alone is insufficient.
- Tests use observable readiness and bounded deadlines, never sleeps for synchronization.
  Per-process limits run in helper children; never lower the test runner's limits.
  Self-kill helpers must wait for kernel termination instead of racing a normal
  exit; writer-error helpers emit stdout in one write to avoid secondary SIGPIPE.
- `AfterStart` owns only consumer resource cleanup. It runs once even on validation failure;
  stop and cancellation must survive callbacks and spawn publication.
- Go may normalize the minimum directive to `1.25.0` to match the pinned dependency.
- Quality measurements: `task mutation` uses `.gremlins.yaml` for root-package mutation
  tests; `task coverage` merges unit and instrumented native-fixture counters and enforces
  `scripts/coverage-threshold.txt`. Instrument the fixture main package too: excluding it
  prevents automatic coverage emission. Run the compiled e2e test binary so `go test`
  cannot replace the fixture's `GOCOVERDIR`.
- `task test:freebsd` starts the pinned FreeBSD KVM guest and runs the working tree without
  git. `sh scripts/bsdvm.sh down` removes the container while preserving its cached disk.
  Every Docker invocation that starts a container must specify `--platform`.
- Address-space helper tests must restore their original soft limit after recording
  the kernel observation, before instrumented `os.Exit` can allocate coverage buffers.
