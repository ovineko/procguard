//go:build e2e

package e2e

import (
	"strings"
	"testing"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

// The parent-limits outcome checks apply to every capability: on
// non-Linux platforms the expectation is "unsupported", and the record must
// still prove that the launch was refused with exactly one AfterStart call
// reporting pid 0. A refactor that gated these checks on capability == "ok"
// silently dropped them for the unsupported traces.
func TestParentLimitsOutcomeAssertion(t *testing.T) {
	unsupportedTraces := []struct {
		name  string
		class Class
		rec   wire.Record
	}{
		{"windows pid leak", Windows, wire.Record{Event: "capability", Calls: 1, PID: 4242}},
		{"bsd pid leak", BSD, wire.Record{Event: "capability", Calls: 1, PID: 4242}},
		{"windows double afterstart", Windows, wire.Record{Event: "capability", Calls: 2}},
	}
	for _, tc := range unsupportedTraces {
		t.Run(tc.name+" rejected", func(t *testing.T) {
			s := Scenario{ID: "parent-limits"}
			err := assertParentLimitsOutcome(s, tc.class, tc.rec, func(string) wire.Record {
				return wire.Record{Event: "observed", PIDs: []int{1}, Alive: []bool{false}}
			})
			if err == nil {
				t.Fatal("defective unsupported trace must be rejected")
			}
		})
	}

	t.Run("linux ok accepted", func(t *testing.T) {
		s := Scenario{ID: "parent-limits"}
		observed := wire.Record{Event: "observed", PIDs: []int{111, 222}, Alive: []bool{false, false}}
		err := assertParentLimitsOutcome(s, Linux, wire.Record{Event: "capability", Calls: 1, PID: 0}, func(string) wire.Record {
			return observed
		})
		if err != nil {
			t.Fatalf("clean linux trace must be accepted: %v", err)
		}
	})

	t.Run("linux live member rejected", func(t *testing.T) {
		s := Scenario{ID: "parent-limits"}
		observed := wire.Record{Event: "observed", PIDs: []int{111}, Alive: []bool{true}}
		err := assertParentLimitsOutcome(s, Linux, wire.Record{Event: "capability", Calls: 1, PID: 0}, func(string) wire.Record {
			return observed
		})
		if err == nil || !strings.Contains(err.Error(), "live processes") {
			t.Fatalf("live members after failed limits must be rejected, got %v", err)
		}
	})

	t.Run("non parent-limits ignored", func(t *testing.T) {
		s := Scenario{ID: "stdin"}
		if err := assertParentLimitsOutcome(s, Windows, wire.Record{Event: "capability", PID: 999, Calls: 5}, func(string) wire.Record {
			return wire.Record{}
		}); err != nil {
			t.Fatalf("non parent-limits scenario must be ignored: %v", err)
		}
	})
}
