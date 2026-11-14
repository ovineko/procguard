//go:build e2e

package main

import (
	"os"
	"testing"
)

// TestMain dispatches fixture subcommand re-execution: the tests start the
// test binary itself in fixture modes, so a recognized subcommand runs the
// fixture main instead of the test suite.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "supervise", "child", "grandchild", "setsid-grandchild", "observer", "anchor", "crash-anchor":
			os.Exit(run())
		}
	}
	os.Exit(m.Run())
}
