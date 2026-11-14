//go:build !linux

package procguard

import (
	"fmt"
	"runtime"
)

func validatePlatformSpec(spec Spec) error {
	if spec.Limits.AddressSpace != 0 || spec.Limits.OOMScoreAdj != nil {
		return fmt.Errorf("parent-side limits: %w", ErrUnsupported)
	}
	if runtime.GOOS == "windows" && len(spec.ExtraFiles) > 0 {
		return fmt.Errorf("extra files: %w", ErrUnsupported)
	}
	return nil
}
func applyChildLimits(_ int, _ Limits) error { return nil }
func setSelfOOM(_ int) error                 { return nil }
