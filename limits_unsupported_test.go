//go:build windows || openbsd

package procguard

import (
	"errors"
	"testing"
)

func TestUnsupportedSelfLimit(t *testing.T) {
	if err := ApplyLimitsToSelf(Limits{AddressSpace: 1 << 30}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
