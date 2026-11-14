//go:build e2e

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func supervise(c config) error {
	switch c.scenario {
	case "repeated-launches", "start-failure", "pid1-reaping":
		return restartLoop(c)
	case "pids-exhaustion":
		return exhaustedPIDs(c)
	case "parent-limits", "oom-decrease":
		return parentLimits(c)
	case "cgroup-v2":
		return cgroupCapability(c)
	case "self-address-space", "self-oom", "extra-files", "stdin":
		return capability(c)
	}
	return superviseTree(c)
}

func superviseTree(c config) error {
	terms, stop := termSignals("exit")
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-terms:
			cancel()
		case <-ctx.Done():
		}
	}()
	if c.scenario == "watch-parent" && runtime.GOOS == "windows" {
		return reportParentWatch(ctx)
	}
	if c.scenario == "empty-unit" {
		c.grace = testsupport.Scale(10 * time.Second)
	}
	return superviseTreeMembers(ctx, cancel, c)
}

type treeRun struct {
	child                              *procguard.Child
	stdoutR, stdoutW, stderrR, stderrW *os.File
	stdoutDrain, stderrDrain           <-chan error
}

func (r *treeRun) closeOutput() {
	for _, file := range []*os.File{r.stdoutR, r.stdoutW, r.stderrR, r.stderrW} {
		if file != nil {
			_ = file.Close()
		}
	}
}

func startTree(ctx context.Context, c config) (*treeRun, error) {
	run := &treeRun{}
	args := treeCommand(c)
	if c.scenario == "immediate-natural-exit" || c.scenario == "immediate-cancel" {
		var err error
		run.stdoutR, run.stdoutW, run.stderrR, run.stderrW, err = newOutputPipes()
		if err != nil {
			return nil, fmt.Errorf("create inherited output pipes: %w", err)
		}
	}
	var calls int
	spec := procguard.Spec{
		Path:            os.Args[0],
		Args:            args,
		GracePeriod:     c.grace,
		KillImmediately: c.scenario == "immediate-natural-exit" || c.scenario == "immediate-cancel",
		AfterStart: func(pid int, err error) {
			calls++
			if err == nil {
				must(mark(c.dir, "launch", "go"))
			}
			r := wire.Record{Event: "after-start", PID: pid, Calls: calls}
			if err != nil {
				r.Error = err.Error()
			}
			emit(r)
		},
	}
	if run.stdoutW != nil {
		spec.Stdout, spec.Stderr = run.stdoutW, run.stderrW
	}
	child, err := procguard.Start(ctx, spec)
	if err != nil {
		run.closeOutput()
		return nil, fmt.Errorf("start tree: %w", err)
	}
	run.child = child
	if run.stdoutW != nil {
		_ = run.stdoutW.Close()
		_ = run.stderrW.Close()
		run.stdoutDrain, run.stderrDrain = drainPipe(run.stdoutR), drainPipe(run.stderrR)
	}
	return run, nil
}

func superviseTreeMembers(ctx context.Context, cancel context.CancelFunc, c config) error {
	run, err := startTree(ctx, c)
	if err != nil {
		return err
	}
	defer run.closeOutput()
	child := run.child
	defer func() { child.Stop(); child.Wait() }()
	emit(wire.Record{Event: "started", PID: child.PID()})
	deadline, finish := bounded(ctx)
	defer finish()
	pids, err := awaitTreeReady(deadline, c, child)
	if err != nil {
		return err
	}
	defer func() {
		for _, pid := range pids {
			_ = hardKill(pid)
		}
	}()
	stopping := time.Now()
	exit, delay, err := stopTree(deadline, cancel, c, run, stopping)
	if err != nil {
		return err
	}
	return reportTreeExit(ctx, pids, exit, delay, c.dir)
}

func awaitTreeReady(deadline context.Context, c config, child *procguard.Child) ([]int, error) {
	if err := until(deadline, func() bool { return exists(filepath.Join(c.dir, strconv.Itoa(child.PID())+".ready")) }); err != nil {
		return nil, err
	}
	pids, err := observedPIDs(c.dir)
	if err != nil {
		return nil, err
	}
	if c.scenario == "setsid-escape" {
		if err := emitIdentities(c.dir, pids); err != nil {
			return nil, err
		}
	}
	emit(wire.Record{Event: "ready", PID: os.Getpid(), PIDs: pids})
	must(mark(c.dir, "supervisor.ready", strconv.Itoa(os.Getpid())))
	return pids, nil
}

func stopTree(deadline context.Context, cancel context.CancelFunc, c config, run *treeRun, stopping time.Time) (procguard.Exit, time.Duration, error) {
	if err := requestStop(deadline, cancel, run.child, c); err != nil {
		return procguard.Exit{}, 0, err
	}
	select {
	case <-run.child.Done():
	case <-deadline.Done():
		if !errors.Is(deadline.Err(), context.Canceled) {
			return procguard.Exit{}, 0, errors.New("tree stop timed out")
		}
		timer := time.NewTimer(testsupport.Scale(wire.Deadline))
		defer timer.Stop()
		select {
		case <-run.child.Done():
		case <-timer.C:
			return procguard.Exit{}, 0, errors.New("tree stop timed out after cancellation")
		}
	}
	exit := run.child.Wait()
	if run.stdoutDrain != nil {
		if err := waitPipeDrains(deadline, run.stdoutDrain, run.stderrDrain); err != nil {
			return procguard.Exit{}, 0, err
		}
	}
	return exit, time.Since(stopping), nil
}

func requestStop(deadline context.Context, cancel context.CancelFunc, child *procguard.Child, c config) error {
	switch c.scenario {
	case "supervisor-death", "watch-parent", "docker-stop":
		select {
		case <-deadline.Done():
			if errors.Is(deadline.Err(), context.DeadlineExceeded) {
				return errors.New("external action timed out")
			}
		case <-time.After(testsupport.Scale(wire.Deadline)):
			return errors.New("external action timed out")
		}
	case "empty-unit":
	case "natural-exit", "immediate-natural-exit":
		must(mark(c.dir, "exit", "exit"))
	case "immediate-cancel":
		cancel()
	default:
		if c.gate != "" {
			if err := until(deadline, func() bool { return exists(c.gate) }); err != nil {
				return err
			}
		}
		child.Stop()
	}
	return nil
}

func reportTreeExit(ctx context.Context, pids []int, exit procguard.Exit, delay time.Duration, dir string) error {
	emit(wire.Record{Event: "exited", Exit: wire.Snapshot(exit), Delay: delay})
	if err := emitMarkers(dir, pids); err != nil {
		return err
	}
	outcome := procguard.Outcome{Reason: procguard.Completed, Last: &exit, Cleanup: exit.Cleanup, Err: exit.Err}
	if ctx.Err() != nil {
		outcome.Reason = procguard.Cancelled
	} else if exit.Requested {
		outcome.Reason = procguard.Stopped
	}
	emit(wire.Record{Event: "outcome", Outcome: wire.OutcomeSnapshot(outcome)})
	state := procguard.State{Launches: 1, Last: &exit}
	emit(wire.Record{Event: "state", State: wire.StateSnapshot(state)})
	return observeExternal(pids)
}

func parentLimits(c config) error {
	score := 100
	inherited, err := inheritedOOM()
	if err != nil {
		return err
	}
	if c.scenario == "oom-decrease" {
		score = inherited - 1
	} else if runtime.GOOS == "linux" {
		score = raisedOOM(inherited)
	}
	before, err := limitValues()
	if err != nil {
		return err
	}
	calls, pid := 0, 0
	child, err := procguard.Start(context.Background(), procguard.Spec{Path: os.Args[0], Args: []string{"child", "-dir", c.dir, "-grandchildren", "0"}, GracePeriod: testsupport.Scale(50 * time.Millisecond), Limits: procguard.Limits{OOMScoreAdj: &score, AddressSpace: wire.AddressSpace}, AfterStart: func(p int, e error) {
		calls++
		pid = p
		r := wire.Record{Event: "after-start", PID: p, Calls: calls}
		if e != nil {
			r.Error = e.Error()
		}
		emit(r)
	}})
	r := wire.Record{Event: "capability", Calls: calls, PID: pid, Permission: errors.Is(err, os.ErrPermission), Unsupported: errors.Is(err, procguard.ErrUnsupported)}
	if err != nil {
		r.Error = err.Error()
	}
	if child != nil {
		defer func() { child.Stop(); child.Wait() }()
		actual, readErr := childLimitValues(pid)
		if readErr != nil {
			return readErr
		}
		actual.BeforeAddressSpace, actual.BeforeOOM, actual.WantedOOM = before.AddressSpace, inherited, score
		r.Limits = &actual
		child.Stop()
		exit := child.Wait()
		r.Exit = wire.Snapshot(exit)
	}
	emit(r)
	if pid > 0 {
		return observeExternal([]int{pid})
	}
	return nil
}

func capability(c config) error {
	if c.scenario == "stdin" {
		return stdinCapability(c)
	}
	if c.scenario != "extra-files" {
		limits := procguard.Limits{AddressSpace: wire.AddressSpace}
		if c.scenario == "self-oom" {
			inherited, err := inheritedOOM()
			if err != nil {
				return err
			}
			score := raisedOOM(inherited)
			limits = procguard.Limits{OOMScoreAdj: &score}
		}
		before, readErr := limitValues()
		if readErr != nil {
			return readErr
		}
		err := procguard.ApplyLimitsToSelf(limits)
		var actual wire.LimitObservation
		if err == nil {
			actual, readErr = limitValues()
			if readErr != nil {
				return readErr
			}
			actual.BeforeAddressSpace, actual.BeforeOOM = before.AddressSpace, before.OOM
			if limits.OOMScoreAdj != nil {
				actual.WantedOOM = *limits.OOMScoreAdj
			}
		}
		r := wire.Record{Event: "capability", Limits: &actual, Unsupported: errors.Is(err, procguard.ErrUnsupported)}
		if err != nil {
			r.Error = err.Error()
		}
		emit(r)
		return nil
	}
	file, err := os.CreateTemp(c.dir, "inherited-")
	if err != nil {
		return fmt.Errorf("create inherited file: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(wire.ExtraPayload); err != nil {
		return fmt.Errorf("prepare descriptor payload: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind descriptor: %w", err)
	}
	child, err := procguard.Start(context.Background(), procguard.Spec{Path: os.Args[0], Args: []string{"child", "-scenario", "extra-file"}, ExtraFiles: []*os.File{file}})
	r := wire.Record{Event: "capability", Unsupported: errors.Is(err, procguard.ErrUnsupported)}
	if err != nil {
		r.Error = err.Error()
	} else {
		exit := child.Wait()
		r.Exit = wire.Snapshot(exit)
		data, readErr := os.ReadFile(file.Name())
		if readErr != nil {
			return fmt.Errorf("read inherited file: %w", readErr)
		}
		r.Data = string(data)
		if string(data) != wire.ExtraAck {
			return errors.New("inherited data mismatch")
		}
	}
	emit(r)
	return nil
}

func stdinCapability(c config) error {
	file, err := os.CreateTemp(c.dir, "stdin-")
	if err != nil {
		return fmt.Errorf("create stdin file: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(wire.StdinPayload); err != nil {
		return fmt.Errorf("prepare stdin payload: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind stdin: %w", err)
	}
	var output bytes.Buffer
	child, err := procguard.Start(context.Background(), procguard.Spec{Path: os.Args[0], Args: []string{"child", "-scenario", "stdin"}, Stdin: file, Stdout: &output})
	r := wire.Record{Event: "capability", Unsupported: errors.Is(err, procguard.ErrUnsupported)}
	if err != nil {
		r.Error = err.Error()
	} else {
		exit := child.Wait()
		r.Exit = wire.Snapshot(exit)
		r.Data = output.String()
		if output.String() != wire.StdinPayload {
			return errors.New("standard input echo mismatch")
		}
	}
	emit(r)
	return nil
}

func restartLoop(c config) error {
	ctx, cancel := consumerContext()
	defer cancel()
	var attempts atomic.Int32
	beforeFD, beforeThreads := 0, 0
	var s *procguard.Supervisor
	var err error
	maxBackoff := time.Millisecond
	if c.scenario == "start-failure" {
		maxBackoff = 4 * time.Second
	}
	s, err = procguard.NewSupervisor(procguard.Options{
		EventBuffer: 512,
		Backoff:     procguard.NewBackoff(maxBackoff, time.Hour),
		Restart:     procguard.RestartPolicy{Mode: procguard.RestartAlways, PreventExitCodes: []int{42}},
		Spec: func(context.Context) (procguard.Spec, error) {
			n := attempts.Add(1)
			if c.scenario == "pid1-reaping" && n > 1 {
				emitZombieSample(c.dir)
			}
			scenario, count, path := restartCommand(c, n)
			if n == 10 {
				beforeFD, beforeThreads, err = resources()
				if err != nil {
					return procguard.Spec{}, err
				}
			}
			return procguard.Spec{Path: path, Args: []string{"child", "-dir", c.dir, "-scenario", scenario, "-grandchildren", strconv.Itoa(count), "-term", "exit"}, GracePeriod: testsupport.Scale(50 * time.Millisecond), AfterStart: func(pid int, e error) {
				r := wire.Record{Event: "after-start", PID: pid, Calls: int(n)}
				if e != nil {
					r.Error = e.Error()
				}
				emit(r)
			}}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("create supervisor: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		failures := 0
		for event := range s.Events() {
			switch event.Kind {
			case procguard.EventStarted:
				emit(wire.Record{Event: "started", PID: event.PID})
			case procguard.EventExited:
				emit(wire.Record{Event: "exited", Exit: wire.Snapshot(*event.Exit)})
			case procguard.EventStartFailed:
				failures++
				emit(wire.Record{Event: "start-failed", Error: event.Err.Error()})
			case procguard.EventRestartScheduled:
				emit(wire.Record{Event: "backoff", Delay: event.Delay})
				if c.scenario == "start-failure" && failures == 3 {
					s.Stop()
				}
			case procguard.EventStopped:
				continue
			}
		}
	}()
	outcome := s.Run(ctx)
	<-done
	state := s.State()
	emit(wire.Record{Event: "outcome", Outcome: wire.OutcomeSnapshot(outcome)})
	emit(wire.Record{Event: "state", State: wire.StateSnapshot(state)})
	if c.scenario == "repeated-launches" {
		fd, threads, err := resources()
		if err != nil {
			return err
		}
		emit(wire.Record{Event: "resources", BeforeFD: beforeFD, AfterFD: fd, BeforeThreads: beforeThreads, AfterThreads: threads})
	}
	if c.scenario == "pid1-reaping" {
		emitZombieSample(c.dir)
		pids, err := observedPIDs(c.dir)
		if err != nil {
			return err
		}
		return observeExternal(pids)
	}
	return nil
}

func restartCommand(c config, n int32) (string, int, string) {
	scenario := "instant"
	if n == 50 {
		scenario = "final"
	}
	count := 0
	if c.scenario == "pid1-reaping" {
		scenario = "crash-tree"
		count = 2
		if n == 3 {
			scenario = "final-tree"
		}
	}
	path := os.Args[0]
	if c.scenario == "start-failure" {
		path = filepath.Join(c.dir, "missing-executable")
	}
	return scenario, count, path
}

func raisedOOM(inherited int) int {
	if inherited >= 1000 {
		must(errors.New("inherited OOM score leaves no observable increase"))
	}
	return min(inherited+100, 1000)
}

func reportParentWatch(ctx context.Context) error {
	_, cancelWatch, watchErr := procguard.WatchParent(ctx, os.Getpid(), 500*time.Millisecond)
	if cancelWatch != nil {
		cancelWatch()
	}
	record := wire.Record{Event: "capability", Unsupported: errors.Is(watchErr, procguard.ErrUnsupported)}
	if watchErr != nil {
		record.Error = watchErr.Error()
	}
	emit(record)
	return nil
}

func treeCommand(c config) []string {
	args := []string{"child", "-start-gate", filepath.Join(c.dir, "launch"), "-dir", c.dir, "-grandchildren", strconv.Itoa(c.count), "-term", "trap", "-delay", c.delay.String()}
	switch c.scenario {
	case "supervisor-death":
		args = append(args, "-term", "record")
	case "tree-termination", "exit-killed", "setsid-escape", "immediate-cancel":
		args = append(args, "-term", "ignore")
	case "empty-unit":
		args = append(args, "-grandchildren", "0", "-scenario", "instant")
	case "watch-parent":
		args = append(args, "-grandchildren", "0", "-watch")
	}
	if c.scenario == "setsid-escape" {
		args = append(args, "-escape")
	}
	if c.scenario == "natural-exit" || c.scenario == "immediate-natural-exit" {
		args = append(args, "-gate", filepath.Join(c.dir, "exit"))
	}
	if c.scenario == "immediate-natural-exit" {
		args = append(args, "-scenario", c.scenario, "-term", "ignore")
	}
	return args
}
