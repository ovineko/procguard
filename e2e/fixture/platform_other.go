//go:build e2e && !linux

package main

import (
	"errors"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func cgroupDelegationDenied(error) bool { return false }

func subreaper() error             { return nil }
func reapOrphans()                 {}
func resources() (int, int, error) { return 0, 0, procguard.ErrUnsupported }
func inheritedOOM() (int, error)   { return 0, nil }

func limitValues() (wire.LimitObservation, error) {
	r, err := selfAddressSpace()
	if errors.Is(err, procguard.ErrUnsupported) {
		return r, nil
	}
	return r, err
}

func childLimitValues(int) (wire.LimitObservation, error) {
	return wire.LimitObservation{}, procguard.ErrUnsupported
}
