//go:build linux

package procguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemainingDeadStatesAndMalformedStat(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "456"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"Z", "X", "x", "malformed"} {
		t.Run(state, func(t *testing.T) {
			stat := "456 (member) " + state + " 1 123"
			live, err := remainingInProc(root, 123, func(string) ([]byte, error) { return []byte(stat), nil }, func(pid int) (int, error) {
				if pid != 456 {
					t.Fatalf("unexpected lookup PID %d", pid)
				}
				return 123, nil
			})
			if live || (err != nil) != (state == "malformed") {
				t.Fatalf("state=%s live=%v err=%v", state, live, err)
			}
		})
	}
}

func TestProcessStatNumericEndpoints(t *testing.T) {
	for _, stat := range []string{
		"1 () S 0 0",
		"01 () S 0 0",
		"9 () S 9 9",
		"2147483647 () S 2147483647 2147483647",
	} {
		if _, _, err := parseProcessStat(stat); err != nil {
			t.Fatalf("valid endpoint %q: %v", stat, err)
		}
	}
	for _, stat := range []string{
		"0 () S 0 0", "+1 () S 0 0", "2147483648 () S 0 0",
		"1 () S -1 0", "1 () S +1 0", "1 () S 2147483648 0",
		"1 () S 0 -1", "1 () S 0 +1", "1 () S 0 2147483648",
	} {
		if _, _, err := parseProcessStat(stat); err == nil {
			t.Fatalf("invalid endpoint accepted: %q", stat)
		}
	}
}
