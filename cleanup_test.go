package procguard

import (
	"errors"
	"testing"
	"time"
)

type cleanupProbe struct {
	remaining bool
	err       error
}

type cleanupStub struct {
	t                      *testing.T
	probes                 []cleanupProbe
	politeErr, killErr     error
	politeCalls, killCalls int
	liveUntil              time.Time
}

func TestCleanupFailuresAndKillObservation(t *testing.T) {
	probeErr := errors.New("group enumeration denied")
	killErr := errors.New("group signal denied")
	politeErr := errors.New("polite signal denied")
	for _, tc := range []struct {
		name                   string
		probes                 []cleanupProbe
		politeErr, killErr     error
		wantErr                error
		wantKilled             bool
		wantState              CleanupState
		politeCalls, killCalls int
	}{
		{"initial-probe-error", []cleanupProbe{{false, probeErr}}, nil, nil, probeErr, true, CleanupConfirmed, 1, 1},
		{"kill-error", []cleanupProbe{{true, nil}}, nil, killErr, killErr, true, CleanupConfirmed, 1, 1},
		{"observe-after-kill", []cleanupProbe{{true, nil}, {true, nil}, {false, nil}}, nil, nil, nil, true, CleanupConfirmed, 1, 1},
		{"later-probe-error", []cleanupProbe{{true, nil}, {false, probeErr}}, nil, nil, probeErr, true, CleanupConfirmed, 1, 1},
		{"polite-error", []cleanupProbe{{true, nil}, {false, nil}}, politeErr, nil, politeErr, true, CleanupConfirmed, 1, 1},
		{"empty", []cleanupProbe{{false, nil}}, nil, nil, nil, false, CleanupConfirmed, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := &cleanupStub{t: t, probes: tc.probes, politeErr: tc.politeErr, killErr: tc.killErr, liveUntil: time.Now().Add(testScale(25 * time.Millisecond))}
			killed, state, err := cleanProcessTree(tree, 0, false, time.Now().Add(-testScale(time.Second)), time.Time{}, false, nil)
			if killed != tc.wantKilled || state != tc.wantState || !errors.Is(err, tc.wantErr) {
				t.Fatalf("killed=%v state=%v err=%v", killed, state, err)
			}
			if len(tree.probes) != 0 || tree.politeCalls != tc.politeCalls || tree.killCalls != tc.killCalls {
				t.Fatalf("remaining probes=%d polite=%d kills=%d", len(tree.probes), tree.politeCalls, tree.killCalls)
			}
		})
	}
}

func TestCleanupConfirmationRetainsAllProbeFailures(t *testing.T) {
	first := errors.New("first membership probe failed")
	second := errors.New("second membership probe failed")
	for _, immediate := range []bool{false, true} {
		tree := &cleanupStub{t: t, probes: []cleanupProbe{{err: first}, {err: second}, {remaining: false}}}
		_, state, err := cleanProcessTree(tree, 0, immediate, time.Now().Add(-testScale(time.Second)), time.Time{}, false, nil)
		if state != CleanupConfirmed || !errors.Is(err, first) || !errors.Is(err, second) {
			t.Fatalf("immediate=%v state=%v err=%v, want confirmed cleanup retaining both probe errors", immediate, state, err)
		}
	}
}

func TestCleanupImmediateSkipsGrace(t *testing.T) {
	tree := &cleanupStub{t: t, probes: []cleanupProbe{{remaining: true}, {remaining: false}}}
	killed, state, err := cleanProcessTree(tree, time.Hour, true, time.Now(), time.Now().Add(time.Hour), false, nil)
	if err != nil || state != CleanupConfirmed || !killed || tree.politeCalls != 0 || tree.killCalls != 1 {
		t.Fatalf("killed=%v state=%v err=%v polite=%d kills=%d", killed, state, err, tree.politeCalls, tree.killCalls)
	}
}

func TestCleanupImmediateProbeErrorStillKills(t *testing.T) {
	probeErr := errors.New("group enumeration denied")
	killErr := errors.New("group signal denied")
	priorErr := errors.New("earlier escalation failed")
	for _, probe := range []error{probeErr, ErrUnsupported} {
		for _, failedKill := range []error{nil, killErr} {
			tree := &cleanupStub{t: t, probes: []cleanupProbe{{err: probe}}, killErr: failedKill, liveUntil: time.Now()}
			killed, state, err := cleanProcessTree(tree, time.Hour, true, time.Now(), time.Time{}, false, priorErr)
			if !killed || state != CleanupConfirmed || tree.killCalls != 1 || tree.politeCalls != 0 {
				t.Fatalf("killed=%v state=%v kills=%d polite=%d", killed, state, tree.killCalls, tree.politeCalls)
			}
			if !errors.Is(err, probe) || !errors.Is(err, priorErr) || (failedKill != nil && !errors.Is(err, failedKill)) {
				t.Fatalf("cleanup lost errors: %v", err)
			}
		}
	}
}

func TestCleanupUnsupportedEnumerationFails(t *testing.T) {
	killErr := errors.New("group signal denied")
	for _, requested := range []bool{false, true} {
		for _, failedKill := range []error{nil, killErr} {
			tree := &cleanupStub{t: t, killErr: failedKill, probes: []cleanupProbe{{err: ErrUnsupported}}, liveUntil: time.Now().Add(2 * escalationDrainWindow)}
			exited := time.Now().Add(-testScale(time.Second))
			deadline := time.Time{}
			wantPolite := 1
			if requested {
				deadline = exited
				wantPolite = 0
			}
			killed, state, err := cleanProcessTree(tree, 0, false, exited, deadline, false, nil)
			if !killed || state != CleanupUnconfirmed || !errors.Is(err, ErrUnsupported) || (failedKill != nil && !errors.Is(err, failedKill)) {
				t.Fatalf("requested=%v killed=%v state=%v err=%v, want failed probe and kill errors", requested, killed, state, err)
			}
			if tree.killCalls != 1 || tree.politeCalls != wantPolite {
				t.Fatalf("requested=%v kills=%d polite=%d, want one kill and %d polite signals", requested, tree.killCalls, tree.politeCalls, wantPolite)
			}
		}
	}
}

func (s *cleanupStub) remaining() (bool, error) {
	s.t.Helper()
	if len(s.probes) == 0 {
		if !s.liveUntil.IsZero() {
			return time.Now().Before(s.liveUntil), nil
		}
		s.t.Fatal("unexpected additional process group probe")
	}
	probe := s.probes[0]
	s.probes = s.probes[1:]
	return probe.remaining, probe.err
}

func (s *cleanupStub) terminate() error {
	s.politeCalls++
	return s.politeErr
}

func (s *cleanupStub) kill() error {
	s.killCalls++
	return s.killErr
}

func TestCleanupFailedEscalationEndsWithError(t *testing.T) {
	killErr := errors.New("membership verification failed")
	for _, tc := range []struct {
		name      string
		immediate bool
		killed    bool
		wantKills int
	}{
		{name: "graceful prior failure", killed: true},
		{name: "immediate prior failure", immediate: true, killed: true},
		{name: "immediate kill failure", immediate: true, wantKills: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			tree := &cleanupStub{t: t, killErr: killErr, liveUntil: started.Add(2 * (escalationDrainWindow + testScale(time.Second)))}
			var priorErr error
			if tc.killed {
				priorErr = killErr
			}
			killed, state, err := cleanProcessTree(tree, 0, tc.immediate, started, time.Time{}, tc.killed, priorErr)
			if elapsed := time.Since(started); elapsed >= escalationDrainWindow+testScale(time.Second) {
				t.Fatalf("failed escalation exceeded bounded drain: %v", elapsed)
			}
			if !killed || state != CleanupUnconfirmed || !errors.Is(err, killErr) {
				t.Fatalf("killed=%v state=%v err=%v, want attempted escalation and kill error", killed, state, err)
			}
			if tree.killCalls != tc.wantKills || tree.politeCalls != 0 {
				t.Fatalf("kills=%d polite=%d, want kills=%d and no polite signal", tree.killCalls, tree.politeCalls, tc.wantKills)
			}
		})
	}
}
