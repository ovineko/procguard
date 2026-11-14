// Package e2eprotocol defines the fixture's JSON line protocol and harness inputs.
package e2eprotocol

import (
	"os"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"github.com/ovineko/procguard"
)

// Deadline bounds fixture operations, including failed readiness handshakes.
var Deadline = testsupport.Scale(20 * time.Second)

// AddressSpace is the requested soft limit in isolated fixture processes.
const AddressSpace = 1 << 40

// ExtraPayload must be read from descriptor 3 before the child acknowledges it.
const ExtraPayload = "descriptor-three-payload"

// ExtraAck replaces the payload only after the child has checked its contents.
const ExtraAck = "descriptor-three-read-ok"

// StdinPayload must be fully echoed from standard input before the scenario passes.
const StdinPayload = "standard-input-payload"

// Exit preserves every procguard Exit field with printable errors and signals.
type Exit struct {
	PID              int       `json:"pid"`
	Code             int       `json:"code"`
	Signal           string    `json:"signal,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	ExitedAt         time.Time `json:"exited_at"`
	Requested        bool      `json:"requested"`
	Killed           bool      `json:"killed"`
	CleanupConfirmed bool      `json:"cleanup_confirmed"`
	Err              string    `json:"err,omitempty"`
}

// Record is one complete JSON line; Event is stable across runners.
type Record struct {
	CauseType         string            `json:"cause_type,omitempty"`
	PGID              int               `json:"pgid,omitempty"`
	SID               int               `json:"sid,omitempty"`
	Limits            *LimitObservation `json:"limits,omitempty"`
	Data              string            `json:"data,omitempty"`
	ResourceExhausted bool              `json:"resource_exhausted,omitempty"`
	Event             string            `json:"event"`
	PID               int               `json:"pid,omitempty"`
	PIDs              []int             `json:"pids,omitempty"`
	Alive             []bool            `json:"alive,omitempty"`
	Zombies           int               `json:"zombies,omitempty"`
	Exit              *Exit             `json:"exit,omitempty"`
	Outcome           *Outcome          `json:"outcome,omitempty"`
	State             *State            `json:"state,omitempty"`
	Error             string            `json:"error,omitempty"`
	Permission        bool              `json:"permission,omitempty"`
	Unsupported       bool              `json:"unsupported,omitempty"`
	Calls             int               `json:"calls,omitempty"`
	Markers           int               `json:"markers,omitempty"`
	ExpectedPID       int               `json:"expected_pid,omitempty"`
	ObservedPID       int               `json:"observed_pid,omitempty"`
	BeforeFD          int               `json:"before_fd,omitempty"`
	AfterFD           int               `json:"after_fd,omitempty"`
	BeforeThreads     int               `json:"before_threads,omitempty"`
	AfterThreads      int               `json:"after_threads,omitempty"`
	Delay             time.Duration     `json:"delay,omitempty"`
}

// LimitObservation records kernel values rather than inferring success from an API return.
type LimitObservation struct {
	BeforeAddressSpace uint64 `json:"before_address_space"`
	AddressSpace       uint64 `json:"address_space"`
	HardAddressSpace   uint64 `json:"hard_address_space"`
	BeforeOOM          int    `json:"before_oom"`
	OOM                int    `json:"oom"`
	WantedOOM          int    `json:"wanted_oom"`
}

// Snapshot projects procguard errors to strings and confirmation to an explicit boolean.
func Snapshot(e procguard.Exit) *Exit {
	r := &Exit{PID: e.PID, Code: e.Code, StartedAt: e.StartedAt, ExitedAt: e.ExitedAt, Requested: e.Requested, Killed: e.Killed, CleanupConfirmed: e.Cleanup == procguard.CleanupConfirmed}
	if e.Err != nil {
		r.Err = e.Err.Error()
	}
	if e.Signal != nil {
		r.Signal = e.Signal.String()
	}
	return r
}

// RequireDocker makes missing Docker a failure in explicit Docker tasks.
func RequireDocker() bool { return os.Getenv("PROCGUARD_E2E_REQUIRE") == "1" }

// QEMU enables smoke profiles only after the owner configures binfmt.
func QEMU() bool { return os.Getenv("PROCGUARD_E2E_QEMU") == "1" }

// CgroupParentEnv names an explicitly delegated host subtree shared with fixtures.
const CgroupParentEnv = "PROCGUARD_E2E_CGROUP_PARENT"

// CgroupParent overrides automatic delegation with a caller-owned cgroup directory.
func CgroupParent() string { return os.Getenv(CgroupParentEnv) }

// Fixture allows guests without Go to use a host-built executable.
func Fixture() string { return os.Getenv("PROCGUARD_E2E_FIXTURE") }

// Root allows precompiled guest tests to find the guarantee table.
func Root() string { return os.Getenv("PROCGUARD_E2E_ROOT") }

// Outcome carries the authoritative result with a JSON-safe exit.
type Outcome struct {
	Reason           procguard.OutcomeReason `json:"reason"`
	Last             *Exit                   `json:"last,omitempty"`
	CleanupConfirmed bool                    `json:"cleanup_confirmed"`
	Err              string                  `json:"err,omitempty"`
}

// State preserves all supervisor counters and the last exit.
type State struct {
	Running       bool          `json:"running"`
	PID           int           `json:"pid"`
	StartedAt     time.Time     `json:"started_at"`
	Launches      int           `json:"launches"`
	Restarts      int           `json:"restarts"`
	Failures      int           `json:"failures"`
	NextDelay     time.Duration `json:"next_delay"`
	Last          *Exit         `json:"last,omitempty"`
	DroppedEvents uint64        `json:"dropped_events"`
}

// OutcomeSnapshot projects cleanup evidence and terminal errors without raw error objects.
func OutcomeSnapshot(o procguard.Outcome) *Outcome {
	r := &Outcome{Reason: o.Reason, CleanupConfirmed: o.Cleanup == procguard.CleanupConfirmed}
	if o.Err != nil {
		r.Err = o.Err.Error()
	}
	if o.Last != nil {
		r.Last = Snapshot(*o.Last)
	}
	return r
}

// StateSnapshot keeps authoritative accounting in the machine-readable protocol.
func StateSnapshot(s procguard.State) *State {
	r := &State{Running: s.Running, PID: s.PID, StartedAt: s.StartedAt, Launches: s.Launches, Restarts: s.Restarts, Failures: s.Failures, NextDelay: s.NextDelay, DroppedEvents: s.DroppedEvents}
	if s.Last != nil {
		r.Last = Snapshot(*s.Last)
	}
	return r
}
