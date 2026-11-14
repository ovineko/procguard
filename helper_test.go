package procguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
)

const helperSwitch = testsupport.HelperKey

var testDeadline = testScale(10 * time.Second)

func testScale(d time.Duration) time.Duration { return testsupport.Scale(d) }

func TestHelper(_ *testing.T) {
	if !helperEnabled() {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(99)
	}
	args = args[1:]
	switch args[0] {
	case "model":
		os.Exit(helperModel(args[1:]))
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "echo":
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(98)
		}
		_, _ = fmt.Fprintln(os.Stdout, dir)
		_, _ = fmt.Fprintln(os.Stdout, helperValue())
		fmt.Fprintln(os.Stderr, "stderr")
	case "fd":
		file := os.NewFile(3, "inherited")
		if file == nil {
			os.Exit(98)
		}
		_, err := file.WriteString("inherited-data")
		if err != nil {
			os.Exit(98)
		}
		_ = file.Close()
	case "cat":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(98)
		}
	case "spam":
		buf := bytes.Repeat([]byte("x"), 4096)
		for {
			if _, err := os.Stdout.Write(buf); err != nil {
				os.Exit(3)
			}
		}
	case "cat-once":
		buf := make([]byte, 1024)
		if _, err := io.ReadFull(os.Stdin, buf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			os.Exit(98)
		}
	case "sleep-sec":
		sec, _ := strconv.Atoi(args[1])
		<-time.After(testScale(time.Duration(sec) * time.Second))
		os.Exit(0)
	case "cgroup-self-max":
		os.Exit(helperCgroupSelfMax())
	case "cgroup-alloc":
		os.Exit(helperCgroupAlloc(args[1:]))
	case "cgroup-spawn":
		os.Exit(helperCgroupSpawn(args[1:]))
	case "cleanup-tree":
		helperCleanupTree(args[1:])
	case "cleanup-member":
		signals := helperSignals(args[1])
		helperWriteFile(args[2], "ready")
		<-signals
		// This timer models real cleanup work; readiness uses the file protocol.
		<-time.After(testScale(300 * time.Millisecond))
		helperWriteFile(args[3], "terminated")
	case "gated-exit":
		helperSignals("graceful")
		helperWriteFile(args[1], "ready")
		helperAwaitFile(args[2])
		code, _ := strconv.Atoi(args[3])
		os.Exit(code)
	case "tree":
		helperAwaitFile(args[1])
		// #nosec G204 G702 -- Re-executes this test binary with arguments supplied by the test harness.
		cmd := exec.Command(testsupport.Executable(), "-test.run=^TestHelper$", "--", "ignore", args[2])
		cmd.Env = helperEnv()
		if err := cmd.Start(); err != nil {
			os.Exit(98)
		}
		helperAwaitFile(args[2])
		_, _ = fmt.Fprintf(os.Stdout, "grand:%d\n", cmd.Process.Pid)
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		<-helperSignals("graceful")
		os.Exit(0)
	case "supervisor":
		spec := helperSpec("wait", args[1])
		if len(args) > 2 {
			spec = helperSpec("watch", strconv.Itoa(os.Getpid()), args[1], args[2])
		}
		child, err := Start(context.Background(), spec)
		if err != nil {
			os.Exit(98)
		}
		helperAwaitFile(args[1])
		_, _ = fmt.Fprintf(os.Stdout, "child:%d\n", child.PID())
		child.Wait()
	case "selfoom":
		helperSelfOOM(args[1:])
	case "selflimits":
		helperSelfLimits(args[1:])
	case "watch":
		helperSignals("graceful")
		parent, _ := strconv.Atoi(args[1])
		ctx, cancel, err := WatchParent(context.Background(), parent, minPollInterval)
		if err != nil {
			os.Exit(98)
		}
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		if len(args) > 2 {
			helperWriteFile(args[2], "ready")
		}
		<-ctx.Done()
		var pe *ParentExitError
		if !errors.As(context.Cause(ctx), &pe) {
			os.Exit(97)
		}
		if len(args) > 3 {
			helperWriteFile(args[3], fmt.Sprintf("%d %d", pe.ExpectedPID, pe.ObservedPID))
		}
		_, _ = fmt.Fprintln(os.Stdout, pe)
		cancel()
	default:
		signals := helperSignals(args[0])
		if len(args) > 1 {
			// #nosec G703 -- The test harness supplies the readiness path in its temporary directory.
			if err := os.WriteFile(args[1], []byte("ready"), 0o600); err != nil {
				os.Exit(98)
			}
		}
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		<-signals
	}
	os.Exit(0)
}

func helperEnabled() bool { return testsupport.Helper() }
func helperValue() string { return testsupport.Value() }
func helperEnv() []string { return append(os.Environ(), helperSwitch+"=1", "GORACE=atexit_sleep_ms=0") }

func helperSpec(args ...string) Spec {
	return Spec{Path: testsupport.Executable(), Args: append([]string{"-test.run=^TestHelper$", "--"}, args...), Env: helperEnv(), GracePeriod: testScale(100 * time.Millisecond)}
}

func helperAwaitFile(path string) {
	timer := time.NewTimer(testDeadline)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		// #nosec G703 -- The test harness supplies the synchronization path in its temporary directory.
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-timer.C:
			panic("helper readiness timed out")
		case <-ticker.C:
		}
	}
}

type observedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *observedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, _ := w.buffer.Write(p)
	return n, nil
}

func (w *observedOutput) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buffer.String() }

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(testDeadline)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-timer.C:
			t.Fatal("observable condition timed out")
		case <-ticker.C:
		}
	}
}

func awaitExit(t *testing.T, c *Child) Exit {
	t.Helper()
	select {
	case <-c.Done():
		return c.Wait()
	case <-time.After(testDeadline):
		t.Fatal("child did not exit")
	}
	return Exit{}
}

func startHelper(t *testing.T, spec Spec) *Child {
	t.Helper()
	c, err := Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(); awaitExit(t, c) })
	return c
}

func awaitReady(t *testing.T, output *observedOutput) {
	t.Helper()
	eventually(t, func() bool { return strings.Contains(output.String(), "ready") })
}

func awaitEvent(t *testing.T, s *Supervisor, kind EventKind) Event {
	t.Helper()
	timer := time.NewTimer(testDeadline)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed before %v", kind)
			}
			if event.Kind == kind {
				return event
			}
		case <-timer.C:
			t.Fatalf("event %v timed out: %+v", kind, s.State())
		}
	}
}

func runSupervisor(ctx context.Context, t *testing.T, opts Options) (*Supervisor, <-chan Outcome) {
	t.Helper()
	s, err := NewSupervisor(opts)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan Outcome, 1)
	go func() { results <- s.Run(ctx) }()
	t.Cleanup(func() { s.Stop(); eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.finished }) })
	return s, results
}

func awaitOutcome(t *testing.T, results <-chan Outcome) Outcome {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(testDeadline):
		t.Fatal("supervisor did not return")
	}
	return Outcome{}
}

func shortBackoff() *Backoff {
	b := NewBackoff(40*time.Millisecond, time.Hour)
	b.base = 5 * time.Millisecond
	return b
}
func readyPath(t *testing.T) string { t.Helper(); return filepath.Join(t.TempDir(), "ready") }

func helperWriteFile(path, value string) {
	// #nosec G703 -- The test harness supplies the output path in its temporary directory.
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		os.Exit(98)
	}
}

func helperCleanupTree(args []string) {
	signals := helperSignals("graceful")
	helperAwaitFile(args[0])
	// #nosec G204 G702 -- Re-executes this test binary with arguments supplied by the test harness.
	cmd := exec.Command(testsupport.Executable(), "-test.run=^TestHelper$", "--", "cleanup-member", args[4], args[1], args[2])
	cmd.Env = helperEnv()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(98)
	}
	helperAwaitFile(args[1])
	_, _ = fmt.Fprintf(os.Stdout, "grand:%d\nready\n", cmd.Process.Pid)
	if args[3] == "stop" {
		<-signals
	} else {
		helperAwaitFile(args[3])
	}
	os.Exit(0)
}

func treeEnumerationAvailable() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" || runtime.GOOS == "dragonfly"
}
