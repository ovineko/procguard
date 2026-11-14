//go:build e2e && linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"golang.org/x/sys/unix"
)

func exhaustedPIDs(_ config) error {
	ctx, cancel := consumerContext()
	defer cancel()
	runtime.GOMAXPROCS(2)
	// Warm the dedicated spawning thread before the cgroup reaches its task limit.
	warm, err := procguard.Start(context.Background(), procguard.Spec{Path: "/bin/sleep", Args: []string{"30"}, GracePeriod: time.Millisecond})
	if err != nil {
		return fmt.Errorf("warm spawn: %w", err)
	}
	warm.Stop()
	warm.Wait()
	var fillers []*exec.Cmd
	defer func() {
		for _, cmd := range fillers {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	for range 128 {
		//nolint:noctx // CommandContext would allocate cancellation machinery while exhausting the cgroup task limit.
		cmd := exec.Command("/bin/sleep", "30")
		if err := cmd.Start(); err != nil {
			if !errors.Is(err, unix.EAGAIN) {
				return fmt.Errorf("fill cgroup: %w", err)
			}
			break
		}
		fillers = append(fillers, cmd)
	}
	if len(fillers) == 128 {
		return errors.New("pids profile did not enforce its task limit")
	}
	calls := 0
	s, err := procguard.NewSupervisor(procguard.Options{EventBuffer: 64, Backoff: procguard.NewBackoff(4*time.Second, time.Hour), Spec: func(context.Context) (procguard.Spec, error) {
		return procguard.Spec{Path: "/bin/sleep", Args: []string{"30"}, GracePeriod: time.Millisecond, AfterStart: func(pid int, e error) {
			calls++
			r := wire.Record{Event: "after-start", PID: pid, Calls: calls, ResourceExhausted: errors.Is(e, unix.EAGAIN)}
			if e != nil {
				r.Error = e.Error()
			}
			emit(r)
		}}, nil
	}})
	if err != nil {
		return fmt.Errorf("create exhaustion supervisor: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		failures := 0
		for e := range s.Events() {
			switch e.Kind {
			case procguard.EventStartFailed:
				failures++
				emit(wire.Record{Event: "start-failed", Error: e.Err.Error(), ResourceExhausted: errors.Is(e.Err, unix.EAGAIN)})
			case procguard.EventStarted:
				s.Stop()
			case procguard.EventRestartScheduled:
				emit(wire.Record{Event: "backoff", Delay: e.Delay})
				if failures == 3 {
					s.Stop()
				}
			case procguard.EventExited, procguard.EventStopped:
				continue
			}
		}
	}()
	outcome := s.Run(ctx)
	<-done
	state := s.State()
	emit(wire.Record{Event: "state", State: wire.StateSnapshot(state)})
	emit(wire.Record{Event: "outcome", Outcome: wire.OutcomeSnapshot(outcome)})
	for _, cmd := range fillers {
		_ = cmd.Process.Kill()
		if err := cmd.Wait(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return fmt.Errorf("reap filler: %w", err)
			}
		}
	}
	pids := make([]int, len(fillers))
	for i, cmd := range fillers {
		pids[i] = cmd.Process.Pid
	}
	fillers = nil
	return observeExternal(pids)
}
