//go:build !windows && !openbsd

package procguard

import (
	"fmt"
	"syscall"
)

// setMemoryLimit caps the address space (RLIMIT_AS) of the current process at
// bytes, clamped to the inherited hard limit.
func setMemoryLimit(bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("memory limit must not be negative, got %d", bytes)
	}

	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &current); err != nil {
		return fmt.Errorf("reading RLIMIT_AS: %w", err)
	}

	hardLimit := current.Max
	limit := syscall.Rlimit{
		Cur: clampToHardLimit(bytes, hardLimit),
		Max: hardLimit,
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &limit); err != nil {
		return fmt.Errorf("setting RLIMIT_AS: %w", err)
	}
	return nil
}

// clampToHardLimit converts a request to the platform's rlimit field type and
// caps it at the inherited hard limit. The conversion is the interesting part:
// the field is signed on FreeBSD and DragonFly and unsigned elsewhere, so the
// comparison has to happen after the conversion, and a negative request has to
// be rejected before it (setMemoryLimit already does, but the guard keeps the
// helper total rather than relying on its only caller).
func clampToHardLimit(bytes int64, hardLimit rlimValue) rlimValue {
	if bytes < 0 {
		return 0
	}

	cur := rlimFromBytes(bytes)
	if cur > hardLimit {
		return hardLimit
	}
	return cur
}
