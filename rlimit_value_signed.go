//go:build freebsd || dragonfly

package procguard

// rlimValue is the integer type of the syscall.Rlimit fields on this platform.
// FreeBSD and DragonFly declare them signed, unlike Linux and Darwin.
type rlimValue = int64

func rlimFromBytes(bytes int64) rlimValue { return bytes }
