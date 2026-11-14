# Installation

Use Go 1.25.0 or newer. Add procguard to your module:

```sh
go get github.com/ovineko/procguard
```

The Go module and source repository are
[`github.com/ovineko/procguard`](https://github.com/ovineko/procguard).
procguard is distributed under the [MIT license](https://github.com/ovineko/procguard/blob/main/LICENSE).
The documentation is available at [procguard.ovineko.com](https://procguard.ovineko.com).

The only dependency is `golang.org/x/sys v0.46.0`. CI cross-builds and vets Linux,
macOS, FreeBSD, OpenBSD, NetBSD, and Windows on amd64 and arm64, plus DragonFly
on amd64. Native tests run on Linux, macOS, and Windows; E2E also runs in FreeBSD,
OpenBSD, and NetBSD guests.
These checks do not make every platform's guarantees identical: read the
[platform contract](../guides/platform-guarantees.md) before choosing a platform.
See [testing](../guides/testing.md) for the execution matrix and its limitations.

In the repository, `go build ./...`, `go vet ./...`, `go test ./...`, and
`go test -race ./...` verify the library and runnable examples.
