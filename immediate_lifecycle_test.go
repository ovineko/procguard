//go:build linux || darwin

package procguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestImmediateNaturalExitKillsReadyDescendant(t *testing.T) {
	dir := t.TempDir()
	gate, ready, marker, exitGate := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
	var out observedOutput
	spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, "natural")
	spec.GracePeriod = 30 * time.Second
	spec.KillImmediately = true
	spec.Stdout, spec.Stderr = &out, &out
	spec.AfterStart = func(_ int, err error) {
		if err == nil {
			if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
				t.Errorf("release descendant start gate: %v", err)
			}
		}
	}
	child := startHelper(t, spec)
	awaitReady(t, &out)
	grandchild := helperGrandchildPID(t, out.String())
	started := time.Now()
	if err := os.WriteFile(exitGate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if elapsed := time.Since(started); elapsed >= testScale(3*time.Second) {
		t.Fatalf("immediate natural-exit cleanup took %v", elapsed)
	}
	if exit.Err != nil || exit.Code != 0 || !exit.Killed || exit.Requested {
		t.Fatalf("exit = %+v", exit)
	}
	if !processGone(t, grandchild) {
		t.Fatalf("descendant %d survived immediate natural-exit cleanup", grandchild)
	}
}

func TestImmediateExitCancelRace(t *testing.T) {
	for range 8 {
		dir := t.TempDir()
		gate, ready, marker, exitGate := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
		var out observedOutput
		ctx, cancel := context.WithCancel(t.Context())
		spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, "natural")
		spec.GracePeriod = 30 * time.Second
		spec.KillImmediately = true
		spec.Stdout, spec.Stderr = &out, &out
		spec.AfterStart = func(_ int, err error) {
			if err == nil {
				if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
					t.Errorf("release descendant start gate: %v", err)
				}
			}
		}
		child, err := Start(ctx, spec)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		awaitReady(t, &out)
		grandchild := helperGrandchildPID(t, out.String())
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		go func() { <-start; _ = os.WriteFile(exitGate, []byte("exit"), 0o600); done <- struct{}{} }()
		go func() { <-start; cancel(); done <- struct{}{} }()
		started := time.Now()
		close(start)
		<-done
		<-done
		exit := awaitExit(t, child)
		if elapsed := time.Since(started); elapsed >= testScale(3*time.Second) {
			t.Fatalf("race cleanup took %v", elapsed)
		}
		if exit.Err != nil || !exit.Killed || !processGone(t, grandchild) {
			t.Fatalf("race cleanup: exit=%+v descendant=%d alive=%t", exit, grandchild, !processGone(t, grandchild))
		}
	}
}

func TestSupervisorImmediatePolicyWaitsForCleanup(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			gate, ready, marker, exitGate := filepath.Join(dir, "start"), filepath.Join(dir, "ready"), filepath.Join(dir, "marker"), filepath.Join(dir, "exit")
			var out observedOutput
			var launches int
			var descendant int
			backoff := NewBackoff(time.Hour, time.Hour)
			childSpec := func(_ context.Context) (Spec, error) {
				launches++
				if launches == 1 {
					spec := helperSpec("cleanup-tree", gate, ready, marker, exitGate, "natural")
					spec.GracePeriod, spec.KillImmediately = 30*time.Second, true
					spec.Stdout, spec.Stderr = &out, &out
					spec.AfterStart = func(_ int, err error) {
						if err == nil {
							if err := os.WriteFile(gate, []byte("start"), 0o600); err != nil {
								t.Errorf("release descendant start gate: %v", err)
							}
						}
					}
					return spec, nil
				}
				if descendant != 0 && !processGone(t, descendant) {
					return Spec{}, errors.New("supervisor launched before old descendant cleanup completed")
				}
				return helperSpec("wait"), nil
			}
			s, results := runSupervisor(t.Context(), t, Options{Spec: childSpec, Backoff: backoff, Restart: RestartPolicy{Mode: RestartAlways}})
			first := awaitEvent(t, s, EventStarted)
			awaitReady(t, &out)
			descendant = helperGrandchildPID(t, out.String())
			if action == "stop" {
				s.Stop()
				outcome := awaitOutcome(t, results)
				if outcome.Reason != Stopped || launches != 1 || !processGone(t, descendant) {
					t.Fatalf("stop outcome=%+v launches=%d descendant=%d", outcome, launches, descendant)
				}
				return
			}
			// Supervisor's immediate argument controls backoff only; the factory's
			// KillImmediately policy controls old-child cleanup.
			s.Restart(true)
			scheduled := awaitEvent(t, s, EventRestartScheduled)
			if scheduled.Delay != 0 {
				t.Fatalf("explicit immediate restart delay = %v", scheduled.Delay)
			}
			second := awaitEvent(t, s, EventStarted)
			if second.PID == first.PID || launches != 2 || !processGone(t, descendant) {
				t.Fatalf("restart started before cleanup: first=%d second=%d launches=%d descendant=%d", first.PID, second.PID, launches, descendant)
			}
			s.Stop()
			if outcome := awaitOutcome(t, results); outcome.Reason != Stopped {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func helperGrandchildPID(t *testing.T, output string) int {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if after, ok := strings.CutPrefix(line, "grand:"); ok {
			pid, err := strconv.Atoi(after)
			if err != nil {
				t.Fatalf("invalid grandchild PID %q: %v", line, err)
			}
			return pid
		}
	}
	t.Fatalf("grandchild PID missing from output %q", output)
	return 0
}
