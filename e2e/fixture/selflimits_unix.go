//go:build e2e && !linux && !windows && !openbsd

package main

import (
	"fmt"
	"syscall"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func selfAddressSpace() (wire.LimitObservation, error) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &limit); err != nil {
		return wire.LimitObservation{}, fmt.Errorf("read self RLIMIT_AS: %w", err)
	}
	return wire.LimitObservation{AddressSpace: limitUint64(limit.Cur), HardAddressSpace: limitUint64(limit.Max)}, nil
}

func limitUint64[T ~int64 | ~uint64](value T) uint64 {
	// #nosec G115 -- Resource limits are nonnegative or the all-bits-set infinity sentinel.
	return uint64(value)
}
