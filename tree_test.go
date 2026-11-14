package procguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProcessTree(t *testing.T) {
	for _, action := range []string{"stop", "leader-exit"} {
		t.Run(action, func(t *testing.T) {
			var out observedOutput
			dir := t.TempDir()
			gate := filepath.Join(dir, "go")
			grandReady := filepath.Join(dir, "grand")
			spec := helperSpec("tree", gate, grandReady)
			spec.Stdout = &out
			spec.AfterStart = func(_ int, err error) {
				if err == nil {
					if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
						t.Error(err)
					}
				}
			}
			c := startHelper(t, spec)
			awaitReady(t, &out)
			line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
			grand, err := strconv.Atoi(strings.TrimPrefix(line, "grand:"))
			if err != nil {
				t.Fatal(err)
			}
			if action == "stop" {
				c.Stop()
			} else if err := c.Signal(os.Kill); err != nil {
				t.Fatal(err)
			}
			e := awaitExit(t, c)
			if e.Err != nil {
				t.Fatal(e.Err)
			}
			if treeEnumerationAvailable() {
				eventually(t, func() bool { return processGone(t, grand) })
			} else if action == "leader-exit" {
				if processGone(t, grand) {
					t.Fatal("fallback unexpectedly terminated descendant")
				}
				process, err := os.FindProcess(grand)
				if err != nil {
					t.Fatal(err)
				}
				if err := process.Kill(); err != nil {
					t.Error(err)
				}
				_ = process.Release()
			}
			eventually(t, func() bool { return processGone(t, grand) })
			c.Stop()
		})
	}
}
