//go:build windows || openbsd

package procguard

import "fmt"

func setMemoryLimit(bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("memory limit must not be negative: %d", bytes)
	}
	return fmt.Errorf("RLIMIT_AS: %w", ErrUnsupported)
}
