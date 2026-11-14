# BSD process ABI constants

`generate.c` emits the Go record-layout constants used by the BSD process-group
adapters. Generate only on a native FreeBSD, OpenBSD, NetBSD, or DragonFly host;
the program rejects other GOOS/GOARCH combinations. It reads the active system
headers, so cross-compiled or assumed layouts are not valid inputs.

For a supported native architecture, create an empty temporary directory and run
from the repository root:

```sh
export TMPDIR="$(mktemp -d)"
cc -Wall -Wextra -Werror internal/bsdabi/generate.c -o "$TMPDIR/procguard-bsdabi"
"$TMPDIR/procguard-bsdabi" freebsd amd64 > zprocabi_freebsd_amd64.go
```

Replace the example target and output name with the matching native GOOS/GOARCH.
For a verification-only comparison, redirect output to a temporary file and use
`cmp` against the committed `zprocabi_<goos>_<goarch>.go` file rather than
redirecting over it. Keep `TMPDIR` fresh for each generation run.

`baselines.json` records the OS release and architecture of the actual host used
to generate each committed file. Populate an entry only after genuine native
header-based generation; do not infer or fabricate release metadata. The output
is deterministic and intentionally contains no host release information.
