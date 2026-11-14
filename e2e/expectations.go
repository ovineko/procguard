//go:build e2e || e2e_docker

// Package e2e shares deployment scenarios and assertions between native and Docker runners.
package e2e

import (
	"fmt"
	"time"
)

// Class separates the BSD kernels from macOS.
type Class string

const (
	// Linux uses procfs enumeration and a parent-death signal.
	Linux Class = "linux"
	// Darwin uses kernel process-group enumeration.
	Darwin Class = "darwin"
	// BSD uses native process-group enumeration and preserves graceful cleanup.
	BSD Class = "bsd"
	// Windows uses job accounting and has no polite stop signal.
	Windows Class = "windows"
)

// Expectation is a contract assertion, independent of procguard implementation branches.
type Expectation struct {
	ParentCause bool
	Subreaper   bool
	Escaped     int
	Limits      string
	MaxWait     time.Duration
	Killed      bool
	Survivors   int
	Markers     int
	Capability  string
	Reason      string
}

// Scenario maps directly to a guarantee-table ID.
type Scenario struct {
	ID           string
	Mode         string
	Expectations map[Class]Expectation
}

// Scenarios is the explicit contract registry. Empty Reason means the scenario must run.
var Scenarios = []Scenario{
	{"tree-termination", "supervise", four(Expectation{Killed: true}, Expectation{Killed: true}, Expectation{Killed: true}, Expectation{Killed: true})},
	{"supervisor-death", "anchor", four(Expectation{Survivors: 3, Markers: 1}, Expectation{Survivors: 3}, Expectation{Survivors: 3}, Expectation{})},
	{"graceful-stop", "supervise", four(Expectation{Markers: 3}, Expectation{Markers: 3}, Expectation{Markers: 3}, Expectation{Killed: true})},
	{"natural-exit", "supervise", four(Expectation{Markers: 2}, Expectation{Markers: 2}, Expectation{Markers: 2}, Expectation{Killed: true})},
	{"empty-unit", "supervise", four(Expectation{MaxWait: time.Second}, Expectation{MaxWait: time.Second}, Expectation{MaxWait: time.Second}, Expectation{MaxWait: time.Second})},
	{"exit-killed", "supervise", four(Expectation{Killed: true}, Expectation{Killed: true}, Expectation{Killed: true}, Expectation{Killed: true})},
	{"parent-limits", "supervise", four(Expectation{Capability: "ok", Limits: "parent"}, Expectation{Capability: "unsupported"}, Expectation{Capability: "unsupported"}, Expectation{Capability: "unsupported"})},
	{"self-address-space", "supervise", four(Expectation{Capability: "ok", Limits: "address-space"}, Expectation{Capability: "error"}, Expectation{Capability: "bsd-self-limit", Limits: "address-space"}, Expectation{Capability: "unsupported"})},
	{"self-oom", "supervise", four(Expectation{Capability: "ok", Limits: "oom"}, Expectation{Capability: "ok"}, Expectation{Capability: "ok"}, Expectation{Capability: "ok"})},
	{"watch-parent", "anchor", four(Expectation{ParentCause: true, Subreaper: true}, Expectation{ParentCause: true}, Expectation{ParentCause: true}, Expectation{Capability: "unsupported"})},
	{"cgroup-v2", "supervise", four(Expectation{Capability: "ok", Reason: "requires a delegated cgroup v2 subtree; set PROCGUARD_E2E_CGROUP_REQUIRE=1 to enforce"}, Expectation{Capability: "unsupported"}, Expectation{Capability: "unsupported"}, Expectation{Capability: "unsupported"})},
	{"extra-files", "supervise", four(Expectation{Capability: "ok"}, Expectation{Capability: "ok"}, Expectation{Capability: "ok"}, Expectation{Capability: "unsupported"})},
	{"stdin", "supervise", four(Expectation{Capability: "ok"}, Expectation{Capability: "ok"}, Expectation{Capability: "ok"}, Expectation{Capability: "ok"})},
	{"setsid-escape", "supervise", four(Expectation{Killed: true, Survivors: 2, Escaped: 2}, Expectation{Killed: true, Survivors: 2, Escaped: 2}, Expectation{Killed: true, Survivors: 2, Escaped: 2}, Expectation{Killed: true})},
	{"start-failure", "supervise", four(Expectation{}, Expectation{}, Expectation{}, Expectation{})},
	{"repeated-launches", "supervise", four(Expectation{}, Expectation{Reason: "fd/thread accounting uses Linux procfs"}, Expectation{Reason: "fd/thread accounting uses Linux procfs"}, Expectation{Reason: "fd/thread accounting uses Linux procfs"})},
	{"pid1-reaping", "supervise", four(Expectation{Reason: "requires a Docker PID namespace"}, Expectation{Reason: "requires a Docker PID namespace"}, Expectation{Reason: "requires a Docker PID namespace"}, Expectation{Reason: "requires a Docker PID namespace"})},
	{"immediate-stop", "supervise", four(Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond})},
	{"immediate-natural-exit", "supervise", four(Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond})},
	{"immediate-cancel", "supervise", four(Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond}, Expectation{Killed: true, MaxWait: 300 * time.Millisecond})},
}

func four(linux, darwin, bsd, windows Expectation) map[Class]Expectation {
	return map[Class]Expectation{Linux: linux, Darwin: darwin, BSD: bsd, Windows: windows}
}

// Platform rejects unknown systems rather than silently assigning Linux guarantees.
func Platform(goos string) (Class, error) {
	switch goos {
	case "linux":
		return Linux, nil
	case "darwin":
		return Darwin, nil
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return BSD, nil
	case "windows":
		return Windows, nil
	default:
		return "", fmt.Errorf("no expectations for %s", goos)
	}
}
