package testsupport

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

// Stacks snapshots goroutine identities, retaining stacks for failure diagnostics.
func Stacks() map[string]string {
	size := 64 * 1024
	for {
		buffer := make([]byte, size)
		n := runtime.Stack(buffer, true)
		if n == len(buffer) {
			size *= 2
			continue
		}
		result := map[string]string{}
		for stack := range strings.SplitSeq(string(buffer[:n]), "\n\n") {
			fields := strings.Fields(stack)
			if len(fields) > 1 {
				result[fields[1]] = stack
			}
		}
		return result
	}
}

// WaitNoLeaks allows cleanup to settle before reporting new procguard goroutines.
func WaitNoLeaks(before map[string]string) error {
	return waitNoLeaks(before, Scale(5*time.Second))
}

func waitNoLeaks(before map[string]string, budget time.Duration) error {
	timer := time.NewTimer(budget)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var leaked []string
		for id, stack := range Stacks() {
			if _, existed := before[id]; !existed && strings.Contains(stack, "github.com/ovineko/procguard") {
				leaked = append(leaked, stack)
			}
		}
		if len(leaked) == 0 {
			return nil
		}
		select {
		case <-timer.C:
			return fmt.Errorf("new procguard goroutines after cleanup:\n%s", strings.Join(leaked, "\n\n"))
		case <-ticker.C:
		}
	}
}
