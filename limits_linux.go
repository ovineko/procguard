//go:build linux

package procguard

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func validatePlatformSpec(_ Spec) error { return nil }
func applyChildLimits(pid int, l Limits) error {
	if l.AddressSpace > 0 {
		var limit unix.Rlimit
		if err := unix.Prlimit(pid, unix.RLIMIT_AS, nil, &limit); err != nil {
			return fmt.Errorf("read child RLIMIT_AS: %w", err)
		}
		limit.Cur = min(uint64(l.AddressSpace), limit.Max)
		if err := unix.Prlimit(pid, unix.RLIMIT_AS, &limit, nil); err != nil {
			return fmt.Errorf("set child RLIMIT_AS: %w", err)
		}
	}
	if l.OOMScoreAdj != nil {
		return writeOOM("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", *l.OOMScoreAdj)
	}
	return nil
}
func setSelfOOM(value int) error { return writeOOM("/proc/self/oom_score_adj", value) }
func writeOOM(path string, value int) error {
	if err := os.WriteFile(path, []byte(strconv.Itoa(value)+"\n"), 0o600); err != nil {
		return fmt.Errorf("set OOM score adjustment: %w", err)
	}
	return nil
}
