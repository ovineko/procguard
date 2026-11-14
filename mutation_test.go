package procguard

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBackoffZeroValueContract(t *testing.T) {
	var b Backoff
	if b.Current() != 0 || b.Failures() != 0 || b.Saturated() {
		t.Fatal("zero backoff has failures")
	}
	if b.Stable(5*time.Minute-time.Nanosecond) || !b.Stable(5*time.Minute) {
		t.Fatal("zero backoff must become stable at five minutes")
	}
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second} {
		if got := b.Next(); got != want {
			t.Fatalf("delay=%v want %v", got, want)
		}
	}
	if !b.Saturated() {
		t.Fatal("zero backoff did not cap at one minute")
	}
}

func TestStartDefaultGraceAndDrainAllowance(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.GracePeriod = 0
	child := startHelper(t, spec)
	if child.grace != 10*time.Second || child.cmd.WaitDelay != 12*time.Second {
		t.Fatalf("default grace=%v drain deadline=%v", child.grace, child.cmd.WaitDelay)
	}
	if exit := awaitExit(t, child); exit.Code != 0 || exit.Err != nil {
		t.Fatalf("default launch: %+v", exit)
	}
}

func TestStartRejectsNegativeLimitBeforeSpawn(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.Limits.AddressSpace = -1
	calls := 0
	spec.AfterStart = func(pid int, err error) {
		calls++
		if pid != 0 || err == nil || !strings.Contains(err.Error(), "address space") {
			t.Errorf("validation callback: pid=%d err=%v", pid, err)
		}
	}
	child, err := Start(t.Context(), spec)
	if child != nil {
		child.Stop()
		awaitExit(t, child)
	}
	if child != nil || err == nil || calls != 1 {
		t.Fatalf("invalid limit: child=%v err=%v callbacks=%d", child, err, calls)
	}
}

func TestParentExitErrorMessage(t *testing.T) {
	err := (&ParentExitError{ExpectedPID: 123, ObservedPID: 456}).Error()
	if err != "parent process died (expected 123, now 456)" {
		t.Fatalf("parent diagnostic: %q", err)
	}
}

func TestDroppedEventsExactCount(t *testing.T) {
	s, err := NewSupervisor(Options{
		Spec:    func(context.Context) (Spec, error) { return helperSpec("exit", "0"), nil },
		Restart: RestartPolicy{Mode: RestartNever}, EventBuffer: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := s.Run(t.Context())
	if outcome.Reason != Completed || outcome.Last == nil || outcome.Last.Code != 0 || outcome.Last.Err != nil {
		t.Fatalf("outcome=%+v", outcome)
	}
	if got := s.State().DroppedEvents; got != 2 {
		t.Fatalf("dropped=%d want 2", got)
	}
	event, ok := <-s.Events()
	if !ok || event.Kind != EventStarted {
		t.Fatalf("first event=%+v open=%v", event, ok)
	}
	if _, ok := <-s.Events(); ok {
		t.Fatal("unexpected event after full buffer")
	}
}

func TestBackoffOddMaximum(t *testing.T) {
	b := NewBackoff(2*time.Second+time.Nanosecond, time.Minute)
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 2*time.Second + time.Nanosecond} {
		if got := b.Next(); got != want {
			t.Fatalf("delay=%v want %v", got, want)
		}
	}
}
