//go:build e2e

// Command fixture exercises consumer behavior without test-binary thread pinning.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

type config struct {
	scenario, dir, term, gate, pids, startGate string
	count                                      int
	delay, grace                               time.Duration
	watch, escape                              bool
}

var outputMu sync.Mutex

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 {
		return 2
	}
	mode := os.Args[1]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	var c config
	fs.StringVar(&c.scenario, "scenario", "graceful-stop", "scenario ID")
	fs.StringVar(&c.dir, "dir", "", "writable marker directory")
	fs.StringVar(&c.term, "term", "exit", "exit, ignore, record, or trap")
	fs.StringVar(&c.startGate, "start-gate", "", "gate before spawning descendants")
	fs.StringVar(&c.gate, "gate", "", "exit gate")
	fs.StringVar(&c.pids, "pids", "", "comma-separated observer PIDs")
	fs.IntVar(&c.count, "grandchildren", 2, "grandchild count")
	fs.DurationVar(&c.delay, "delay", testsupport.Scale(100*time.Millisecond), "cleanup work duration")
	fs.DurationVar(&c.grace, "grace", testsupport.Scale(700*time.Millisecond), "unit grace")
	fs.BoolVar(&c.watch, "watch", false, "cooperative parent watch")
	fs.BoolVar(&c.escape, "escape", false, "escape process group")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 2
	}
	if c.dir == "" {
		var err error
		c.dir, err = os.MkdirTemp("", "procguard-e2e-")
		must(err)
		defer func() { _ = os.RemoveAll(c.dir) }()
	}
	var err error
	switch mode {
	case "supervise":
		if c.scenario == "immediate-stop" {
			err = immediateStop(c)
		} else {
			err = supervise(c)
		}
	case "child", "grandchild", "setsid-grandchild":
		err = childMode(c, mode)
	case "observer":
		err = observe(c)
	case "anchor":
		err = anchor(c)
	case "crash-anchor":
		err = crashAnchor(c)
	default:
		err = fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		emit(wire.Record{Event: "error", Error: err.Error()})
		return 2
	}
	return 0
}

func emit(r wire.Record) {
	outputMu.Lock()
	defer outputMu.Unlock()
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		panic(err)
	}
}

func must(err error) {
	if err != nil {
		emit(wire.Record{Event: "error", Error: err.Error()})
		os.Exit(2)
	}
}

func mark(dir, name, value string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func until(ctx context.Context, condition func() bool) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture deadline: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func bounded(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, testsupport.Scale(wire.Deadline))
}

func newOutputPipes() (stdoutR, stdoutW, stderrR, stderrW *os.File, err error) {
	stdoutR, stdoutW, err = os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderrR, stderrW, err = os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, nil, nil, nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	return stdoutR, stdoutW, stderrR, stderrW, nil
}

func drainPipe(reader *os.File) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		done <- err
	}()
	return done
}

func waitPipeDrains(ctx context.Context, drains ...<-chan error) error {
	for _, done := range drains {
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("drain inherited output: %w", err)
			}
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				return fmt.Errorf("inherited output remained open: %w", ctx.Err())
			}
			timer := time.NewTimer(testsupport.Scale(wire.Deadline))
			defer timer.Stop()
			select {
			case err := <-done:
				if err != nil {
					return fmt.Errorf("drain inherited output: %w", err)
				}
			case <-timer.C:
				return errors.New("inherited output remained open after cancellation")
			}
		}
	}
	return nil
}

//nolint:noctx // Scenario observers own teardown; descendants must survive intentional parent termination.
func command(args ...string) *exec.Cmd {
	// #nosec G204 G702 -- The fixture only re-executes itself with harness-owned arguments.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func childMode(c config, mode string) error {
	switch c.scenario {
	case "extra-file":
		return extraFile()
	case "stdin":
		return stdinEcho()
	}
	return childProcess(c, mode)
}

func childProcess(c config, mode string) error {
	terms, stop := termSignals(c.term)
	defer stop()
	if mode == "setsid-grandchild" {
		if err := escapeSession(); err != nil {
			return err
		}
	}
	if mode != "child" {
		c.count = 0
	}
	ctx, cancel := bounded(context.Background())
	defer cancel()
	if c.startGate != "" {
		if err := until(ctx, func() bool { return exists(c.startGate) }); err != nil {
			return err
		}
	}
	children, err := spawnDescendants(ctx, c)
	if err != nil {
		return err
	}
	if c.scenario == "immediate-natural-exit" {
		return waitImmediateNaturalExit(ctx, c, children)
	}
	if c.watch {
		return watchParentChild(c)
	}
	if err := reportChildReady(c, children); err != nil {
		return err
	}
	return waitChildExit(ctx, terms, c, children)
}

func spawnDescendants(ctx context.Context, c config) ([]*exec.Cmd, error) {
	children := make([]*exec.Cmd, 0, c.count)
	for range c.count {
		mode := "grandchild"
		if c.escape {
			mode = "setsid-grandchild"
		}
		cmd := command(mode, "-dir", c.dir, "-term", c.term, "-delay", c.delay.String())
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("spawn grandchild: %w", err)
		}
		children = append(children, cmd)
	}
	for _, cmd := range children {
		if err := until(ctx, func() bool { return exists(filepath.Join(c.dir, strconv.Itoa(cmd.Process.Pid)+".ready")) }); err != nil {
			return nil, err
		}
	}
	return children, nil
}

func waitImmediateNaturalExit(ctx context.Context, c config, children []*exec.Cmd) error {
	if err := reportChildReady(c, children); err != nil {
		return err
	}
	if err := mark(c.dir, "output-held", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	return until(ctx, func() bool { return exists(filepath.Join(c.dir, "exit")) })
}

func watchParentChild(c config) error {
	watch, cancelWatch, err := procguard.WatchParent(context.Background(), os.Getppid(), 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("watch parent: %w", err)
	}
	defer cancelWatch()
	must(mark(c.dir, strconv.Itoa(os.Getpid())+".ready", "ready"))
	<-watch.Done()
	var cause *procguard.ParentExitError
	record := wire.Record{Event: "parent-exit", CauseType: fmt.Sprintf("%T", context.Cause(watch))}
	if errors.As(context.Cause(watch), &cause) {
		record.ExpectedPID, record.ObservedPID = cause.ExpectedPID, cause.ObservedPID
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode cause: %w", err)
	}
	return mark(c.dir, "cause", string(data))
}

func reportChildReady(c config, children []*exec.Cmd) error {
	identity, err := sessionIdentity()
	if err != nil {
		return err
	}
	identity.Event, identity.PID = "identity", os.Getpid()
	data, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("encode identity: %w", err)
	}
	if err := mark(c.dir, strconv.Itoa(os.Getpid())+".ready", string(data)); err != nil {
		return err
	}
	pids := make([]int, len(children))
	for i, cmd := range children {
		pids[i] = cmd.Process.Pid
	}
	emit(wire.Record{Event: "ready", PID: os.Getpid(), PIDs: pids})
	return nil
}

func immediateStop(c config) error {
	stdoutR, stdoutW, stderrR, stderrW, err := newOutputPipes()
	if err != nil {
		return fmt.Errorf("create inherited output pipes: %w", err)
	}
	defer func() {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	}()
	args := []string{"child", "-scenario", c.scenario, "-start-gate", filepath.Join(c.dir, "launch"), "-dir", c.dir, "-grandchildren", strconv.Itoa(c.count), "-term", "ignore", "-delay", c.delay.String()}
	ctx := context.Background()
	child, err := procguard.Start(ctx, procguard.Spec{
		Path:            os.Args[0],
		Args:            args,
		GracePeriod:     c.grace,
		KillImmediately: true,
		Stdout:          stdoutW,
		Stderr:          stderrW,
	})
	if err != nil {
		return fmt.Errorf("start immediate-stop tree: %w", err)
	}
	defer func() { child.Stop(); child.Wait() }()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutDrain, stderrDrain := drainPipe(stdoutR), drainPipe(stderrR)
	emit(wire.Record{Event: "started", PID: child.PID()})
	deadline, finish := bounded(ctx)
	defer finish()
	must(mark(c.dir, "launch", "go"))
	if err := until(deadline, func() bool { return exists(filepath.Join(c.dir, strconv.Itoa(child.PID())+".ready")) }); err != nil {
		return err
	}
	pids, err := observedPIDs(c.dir)
	if err != nil {
		return err
	}
	defer func() {
		for _, pid := range pids {
			_ = hardKill(pid)
		}
	}()
	emit(wire.Record{Event: "ready", PID: os.Getpid(), PIDs: pids})
	stopping := time.Now()
	child.Stop()
	select {
	case <-child.Done():
	case <-deadline.Done():
		return errors.New("immediate tree stop timed out")
	}
	exit := child.Wait()
	if err := waitPipeDrains(deadline, stdoutDrain, stderrDrain); err != nil {
		return err
	}
	emit(wire.Record{Event: "exited", Exit: wire.Snapshot(exit), Delay: time.Since(stopping)})
	if err := emitMarkers(c.dir, pids); err != nil {
		return err
	}
	stopped := procguard.Outcome{Reason: procguard.Stopped, Last: &exit, Cleanup: exit.Cleanup, Err: exit.Err}
	emit(wire.Record{Event: "outcome", Outcome: wire.OutcomeSnapshot(stopped)})
	emit(wire.Record{Event: "state", State: wire.StateSnapshot(procguard.State{Launches: 1, Last: &exit})})
	return observeExternal(pids)
}

func stdinEcho() error {
	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		return fmt.Errorf("echo standard input: %w", err)
	}
	return nil
}

func waitChildExit(ctx context.Context, terms <-chan os.Signal, c config, children []*exec.Cmd) error {
	if c.gate != "" {
		if err := until(ctx, func() bool { return exists(c.gate) }); err != nil {
			return err
		}
		return nil
	}
	if c.scenario == "instant" {
		return nil
	}
	if c.scenario == "final" || c.scenario == "final-tree" {
		os.Exit(42)
	}
	if c.scenario == "crash-tree" {
		os.Exit(1)
	}
	select {
	case <-terms:
	case <-ctx.Done():
		return errors.New("child deadline expired")
	}
	if c.term == "record" {
		must(mark(c.dir, strconv.Itoa(os.Getpid())+".term", "received"))
		<-ctx.Done()
		return errors.New("recording child deadline expired")
	}
	if c.term == "trap" {
		timer := time.NewTimer(c.delay)
		defer timer.Stop()
		<-timer.C
	}
	must(mark(c.dir, strconv.Itoa(os.Getpid())+".term", "terminated"))
	for _, cmd := range children {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("reap grandchild: %w", err)
		}
	}
	return nil
}

func consumerContext() (context.Context, context.CancelFunc) {
	ctx, cancel := bounded(context.Background())
	terms, stop := termSignals("exit")
	go func() {
		select {
		case <-terms:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { stop(); cancel() }
}
