//go:build e2e

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

// Negative controls for the cgroup E2E outcome assertion. The original
// defect: the fixture emitted a failing second capability record that no
// consumer read, and the harness asserted only the first record — the
// original defective trace passed. The new assertion must reject every
// defective trace shape and accept the clean one.
func TestCgroupOutcomeAssertionRejectsDefectiveTraces(t *testing.T) {
	goodCapability := wire.Record{Event: "capability", Calls: 1, PID: 4242}
	goodExit := wire.Record{Event: "exited", Exit: &wire.Exit{Code: 0, CleanupConfirmed: true}, Data: ""}

	t.Run("clean trace accepted", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "leaf")
		// The leaf path must be absent after the unit finished.
		ok := goodExit
		ok.Data = leaf
		if err := assertCgroupOutcome(goodCapability, findOf(ok)); err != nil {
			t.Fatalf("clean trace rejected: %v", err)
		}
	})

	t.Run("original defect trace rejected", func(t *testing.T) {
		defective := wire.Record{Event: "exited", Exit: &wire.Exit{Code: 137}, Error: "cgroup child exit: code=137", Data: ""}
		// Old assertion logic: only the capability record was checked —
		// the original defect trace passes it, which is exactly why the
		// terminal assertion had to be added.
		if capErr := capabilityOnly(goodCapability); capErr == nil {
			t.Log("old capability-only logic accepts the defective trace, as reported")
		}
		err := assertCgroupOutcome(goodCapability, findOf(defective))
		if err == nil {
			t.Fatalf("defective exit must be rejected, got %v", err)
		}
	})

	t.Run("cleanup error rejected", func(t *testing.T) {
		// The fixture reports a cleanup failure on the terminal record
		// itself; the assertion must surface it even when the leaf path is
		// already gone.
		cleanupFailure := goodExit
		cleanupFailure.Exit = &wire.Exit{Code: 0, CleanupConfirmed: true, Err: "cleanup failed"}
		cleanupFailure.Data = filepath.Join(t.TempDir(), "leaf")
		err := assertCgroupOutcome(goodCapability, findOf(cleanupFailure))
		if err == nil {
			t.Fatalf("reported cleanup failure must be rejected, got %v", err)
		}
	})

	t.Run("leftover leaf rejected", func(t *testing.T) {
		leaf := filepath.Join(t.TempDir(), "leaf")
		if err := os.WriteFile(leaf, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		leftover := goodExit
		leftover.Data = leaf
		err := assertCgroupOutcome(goodCapability, findOf(leftover))
		if err == nil {
			t.Fatalf("leftover leaf must be rejected, got %v", err)
		}
	})

	t.Run("terminal record error rejected", func(t *testing.T) {
		terminalError := goodExit
		terminalError.Data = filepath.Join(t.TempDir(), "leaf")
		terminalError.Error = "launch leaf removal unverifiable"
		err := assertCgroupOutcome(goodCapability, findOf(terminalError))
		if err == nil {
			t.Fatalf("terminal record error must be rejected, got %v", err)
		}
	})

	t.Run("missing terminal record rejected", func(t *testing.T) {
		err := assertCgroupOutcome(goodCapability, func(string) wire.Record {
			return wire.Record{Event: "capability", PID: 1}
		})
		if err == nil {
			t.Fatalf("missing terminal record must be rejected, got %v", err)
		}
	})
}

func capabilityOnly(capability wire.Record) error {
	if capability.Error != "" || capability.Unsupported {
		return errors.New("capability failed")
	}
	return nil
}

func findOf(record wire.Record) func(string) wire.Record {
	return func(event string) wire.Record {
		if record.Event == event {
			return record
		}
		return wire.Record{Event: "missing", Error: "no " + event}
	}
}
