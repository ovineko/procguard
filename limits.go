package procguard

import (
	"errors"
	"fmt"
)

// ErrUnsupported identifies a platform capability that cannot be provided.
var ErrUnsupported = errors.New("procguard: unsupported on this platform")

// Limits are opt-in. AddressSpace is bytes (zero leaves it unchanged);
// OOMScoreAdj is Linux-only. Parent-side limits are available only on Linux.
type Limits struct {
	AddressSpace int64
	OOMScoreAdj  *int
}

// ApplyLimitsToSelf is for cooperating children, including FreeBSD children
// whose parents cannot set their RLIMIT_AS. Off Linux OOMScoreAdj is a no-op,
// matching the extracted behavior. Darwin may reject RLIMIT_AS with EINVAL.
// Multiple limits are applied in order and are not rolled back on failure.
func ApplyLimitsToSelf(l Limits) error {
	if err := l.validate(); err != nil {
		return err
	}
	if l.AddressSpace != 0 {
		if err := setMemoryLimit(l.AddressSpace); err != nil {
			return err
		}
	}
	if l.OOMScoreAdj != nil {
		return setSelfOOM(*l.OOMScoreAdj)
	}
	return nil
}

func (l Limits) validate() error {
	if l.AddressSpace < 0 {
		return fmt.Errorf("address space must not be negative: %d", l.AddressSpace)
	}
	return nil
}
