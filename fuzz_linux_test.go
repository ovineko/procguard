//go:build linux

package procguard

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var statPrefix = regexp.MustCompile(`(?s)^[0-9]+ \(.*\) [RSDZTtXxKWPI]\s+[0-9]+\s+[0-9]+(?:\s|$)`)

func FuzzParseProcStat(f *testing.F) {
	f.Fuzz(func(t *testing.T, raw, comm string, pid, ppid, pgrp uint32, state uint8) {
		group, status, err := parseProcessStat(raw)
		if err == nil && (!statPrefix.MatchString(raw) || group < 0 || !strings.ContainsRune("RSDZTtXxKWPI", rune(status))) {
			t.Fatalf("malformed stat accepted: %q -> %d %c", raw, group, status)
		}
		states := "RSDZTtXxKWPI"
		wantState := states[int(state)%len(states)]
		// Kernel PIDs fit signed 32 bits even when the host test runner is 64-bit.
		pid, ppid, pgrp = pid%2147483647+1, ppid%2147483648, pgrp%2147483648
		line := fmt.Sprintf("%d (%s) %c %d %d", pid, comm, wantState, ppid, pgrp)
		group, status, err = parseProcessStat(line)
		if err != nil || group != int(pgrp) || status != wantState {
			t.Fatalf("round trip %q: %d %c %v", line, group, status, err)
		}
	})
}

func TestParseProcessStatMalformedPrefix(t *testing.T) {
	for _, line := range []string{
		") S 1 2", "2147483648 (comm) S 1 2", "1 (comm) S 2147483648 2", "1 (comm) S 1 2147483648", "x (comm) S 1 2", "0 (comm) S 1 2", "1 comm) S 1 2",
		"1 (comm) ? 1 2", "1 (comm) S nope 2", "1 (comm) S -1 2", "1 (comm) S 1 -2", "1 (comm)S 1 2",
		"1 (comm) S 1 " + strconv.FormatUint(^uint64(0), 10),
	} {
		if _, _, err := parseProcessStat(line); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}
