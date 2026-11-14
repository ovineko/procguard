# procguard

procguard provides cross-platform supervision and lifecycle management for child OS processes in Go.

Use `Start` for one launch or `Supervisor` for a restart loop. Each supervisor owns
one logical child; create several supervisors to manage independent binaries.
The consumer decides what an exit code, readiness notification, or diagnostic signal means.

Start with [installation](get-started/installation.md) and the
[quick start](get-started/quick-start.md). Read the [platform contract](guides/platform-guarantees.md)
before depending on orphan prevention or limits, then consult the [API](reference/api.md).

This library provides no init system, service installation, self-update, daemon
framework, container supervision, distributed jobs, health checks, or logging framework.
