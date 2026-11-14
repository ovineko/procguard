//go:build e2e && (windows || openbsd)

package main

import (
	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func selfAddressSpace() (wire.LimitObservation, error) {
	return wire.LimitObservation{}, procguard.ErrUnsupported
}
