//go:build !windows && !openbsd && !freebsd && !dragonfly

package procguard

// rlimValue is the integer type of the syscall.Rlimit fields on this platform.
type rlimValue = uint64

func rlimFromBytes(bytes int64) rlimValue {
	// #nosec G115 -- clampToHardLimit rejects negative requests before converting to the unsigned field type.
	return uint64(bytes)
}
