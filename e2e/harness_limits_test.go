//go:build e2e || e2e_docker

package e2e

import (
	"testing"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func TestDarwinSelfLimitRequiresKernelEvidence(t *testing.T) {
	const before = uint64(4 << 40)
	for _, tc := range []struct {
		name    string
		record  wire.Record
		wantErr bool
	}{
		{"accepted and observed", wire.Record{Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: wire.AddressSpace, HardAddressSpace: before}}, false},
		{"clamped to hard limit", wire.Record{Limits: &wire.LimitObservation{BeforeAddressSpace: 256 << 30, AddressSpace: 512 << 30, HardAddressSpace: 512 << 30}}, false},
		{"EINVAL leaves state unchanged", wire.Record{Error: "invalid argument", Invalid: true, Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: before, HardAddressSpace: before}}, false},
		{"unrelated rejection", wire.Record{Error: "permission denied", Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: before, HardAddressSpace: before}}, true},
		{"EINVAL with changed state", wire.Record{Error: "invalid argument", Invalid: true, Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: wire.AddressSpace, HardAddressSpace: before}}, true},
		{"missing observation", wire.Record{}, true},
		{"unverified success", wire.Record{Limits: &wire.LimitObservation{}}, true},
		{"setter no-op", wire.Record{Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: before, HardAddressSpace: before}}, true},
		{"wrong soft limit", wire.Record{Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: wire.AddressSpace / 2, HardAddressSpace: before}}, true},
		{"invalid flag without error", wire.Record{Invalid: true, Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: wire.AddressSpace, HardAddressSpace: before}}, true},
		{"unsupported is not EINVAL", wire.Record{Unsupported: true, Error: "unsupported", Limits: &wire.LimitObservation{BeforeAddressSpace: before, AddressSpace: before, HardAddressSpace: before}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := assertDarwinSelfLimit(tc.record); (err != nil) != tc.wantErr {
				t.Fatalf("assertDarwinSelfLimit error=%v wantErr=%t", err, tc.wantErr)
			}
		})
	}
}
