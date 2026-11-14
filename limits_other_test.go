//go:build !linux

package procguard

import (
	"errors"
	"os"
	"testing"
)

func TestUnsupportedParentLimits(t *testing.T) {
	score := 100
	for _, limits := range []Limits{{AddressSpace: 1 << 30}, {OOMScoreAdj: &score}} {
		spec := helperSpec("wait")
		spec.Limits = limits
		called := false
		spec.AfterStart = func(pid int, err error) {
			called = true
			if pid != 0 || !errors.Is(err, ErrUnsupported) {
				t.Errorf("callback: %d %v", pid, err)
			}
		}
		c, err := Start(t.Context(), spec)
		if c != nil || !errors.Is(err, ErrUnsupported) || !called {
			t.Fatalf("start: %v %v", c, err)
		}
	}
	if err := ApplyLimitsToSelf(Limits{OOMScoreAdj: &score}); err != nil {
		t.Fatal(err)
	}
}

func helperSelfOOM(_ []string) { os.Exit(98) }
