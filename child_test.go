package procguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
)

func TestImmediateStop(t *testing.T) {
	spec := helperSpec("sleep-sec", "30")
	spec.GracePeriod = time.Hour
	spec.KillImmediately = true
	child := startHelper(t, spec)
	child.Stop()
	exit := awaitExit(t, child)
	if exit.Err != nil || !exit.Killed || !exit.Requested {
		t.Fatalf("exit = %+v", exit)
	}
}

func TestImmediateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	spec := helperSpec("sleep-sec", "30")
	spec.GracePeriod = time.Hour
	spec.KillImmediately = true
	child, err := Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	exit := awaitExit(t, child)
	if exit.Err != nil || !exit.Killed || !exit.Requested {
		t.Fatalf("exit = %+v", exit)
	}
}

func TestImmediateNaturalExitEmptyUnit(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.KillImmediately = true
	exit := awaitExit(t, startHelper(t, spec))
	if exit.Err != nil || exit.Code != 0 || exit.Killed || exit.Requested {
		t.Fatalf("exit = %+v", exit)
	}
}

func TestStartTreeSetupFailureCleansProcess(t *testing.T) {
	wantErr := errors.New("injected tree setup failure")
	spec := helperSpec("sleep-sec", "30")
	var callbackPID, calls int
	var callbackErr error
	spec.AfterStart = func(pid int, err error) { callbackPID, callbackErr, calls = pid, err, calls+1 }
	_, err := startWith(t.Context(), spec, launchHooks{
		newTree: func(int, *cgroupResources) (*processTree, error) {
			return nil, wantErr
		},
	})
	if !errors.Is(err, wantErr) || callbackPID == 0 || calls != 1 || !errors.Is(callbackErr, wantErr) {
		t.Fatalf("start err=%v callback pid=%d calls=%d err=%v", err, callbackPID, calls, callbackErr)
	}
	if !processGone(t, callbackPID) {
		t.Fatalf("failed launch process %d survived cleanup", callbackPID)
	}
}

func TestChildExit(t *testing.T) {
	for _, code := range []string{"0", "23"} {
		t.Run(code, func(t *testing.T) {
			var calls int
			spec := helperSpec("exit", code)
			spec.AfterStart = func(pid int, err error) {
				calls++
				if pid <= 0 || err != nil {
					t.Errorf("callback: %d %v", pid, err)
				}
			}
			c := startHelper(t, spec)
			e := awaitExit(t, c)
			if e.Err != nil || e.Requested || e.Killed || e.PID != c.PID() || e.Runtime() < 0 || e.StartedAt != c.StartedAt() {
				t.Fatalf("exit: %+v", e)
			}
			if (code == "0" && e.Code != 0) || (code == "23" && e.Code != 23) || calls != 1 {
				t.Fatalf("code/callback: %+v %d", e, calls)
			}
			var wg sync.WaitGroup
			for range 10 {
				wg.Go(func() {
					if c.Wait() != e {
						t.Error("Wait changed")
					}
					c.Stop()
				})
			}
			wg.Wait()
			if !errors.Is(c.Signal(os.Interrupt), os.ErrProcessDone) {
				t.Error("signal after exit")
			}
		})
	}
}

func TestStartFailureCallback(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			spec := helperSpec("exit", "0")
			switch kind {
			case "missing":
				spec.Path = filepath.Join(t.TempDir(), "missing")
			case "invalid":
				spec.GracePeriod = -1
			case "cancelled":
				cancel()
			}
			var calls int
			spec.AfterStart = func(pid int, err error) {
				calls++
				if pid != 0 || err == nil {
					t.Errorf("callback: %d %v", pid, err)
				}
			}
			c, err := Start(ctx, spec)
			if c != nil || err == nil || calls != 1 {
				t.Fatalf("start: %v %v %d", c, err, calls)
			}
		})
	}
}

func TestChildIOEnvDir(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "inherit"}[inherit], func(t *testing.T) {
			t.Setenv(helperSwitch, "1")
			t.Setenv(testsupport.ValueKey, "env-value")
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			var stdout, stderr observedOutput
			spec := helperSpec("echo")
			spec.Dir = t.TempDir()
			spec.Stdout, spec.Stderr = &stdout, &stderr
			if inherit {
				spec.Env = nil
			}
			e := awaitExit(t, startHelper(t, spec))
			if e.Code != 0 || !strings.Contains(stdout.String(), spec.Dir) || !strings.Contains(stdout.String(), "env-value") || strings.TrimSpace(stderr.String()) != "stderr" {
				t.Fatalf("exit/output: %+v %q %q", e, stdout.String(), stderr.String())
			}
		})
	}
}

func TestChildStdin(t *testing.T) {
	for _, mode := range []string{"reader", "file", "pipe"} {
		t.Run(mode, func(t *testing.T) {
			payload := "stdin-payload-" + mode
			spec := helperSpec("cat")
			var output observedOutput
			spec.Stdout = &output
			switch mode {
			case "reader":
				spec.Stdin = strings.NewReader(payload)
			case "file":
				file, err := os.CreateTemp(t.TempDir(), "stdin-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = file.Close() })
				if _, err := file.WriteString(payload); err != nil {
					t.Fatal(err)
				}
				if _, err := file.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				spec.Stdin = file
			case "pipe":
				pr, pw, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
				spec.Stdin = pr
				go func() {
					_, _ = pw.WriteString(payload)
					_ = pw.Close()
				}()
			}
			e := awaitExit(t, startHelper(t, spec))
			if e.Code != 0 || e.Err != nil {
				t.Fatalf("exit: %+v", e)
			}
			if got := output.String(); got != payload {
				t.Fatalf("stdin echo: %q, want %q", got, payload)
			}
		})
	}
}

func TestChildPipeChainEarlyExit(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	sourceSpec, destSpec := helperSpec("spam"), helperSpec("cat-once")
	sourceSpec.Stdout = pw
	destSpec.Stdin = pr
	source := startHelper(t, sourceSpec)
	destination := startHelper(t, destSpec)
	_ = pw.Close()
	_ = pr.Close()
	de := awaitExit(t, destination)
	if de.Code != 0 || de.Err != nil {
		t.Fatalf("destination exit: %+v", de)
	}
	se := awaitExit(t, source)
	if se.Requested {
		t.Fatalf("source stop was requested before EPIPE exit: %+v", se)
	}
	if runtime.GOOS == "windows" {
		if se.Code != 3 {
			t.Fatalf("source exit: %+v", se)
		}
		return
	}
	if se.Code != -1 || se.Signal != syscall.SIGPIPE {
		t.Fatalf("source exit: %+v", se)
	}
}

func TestChildCancellation(t *testing.T) {
	for _, action := range []string{"stop", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var output observedOutput
			spec := helperSpec("wait")
			spec.Stdout = &output
			c, err := Start(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Stop(); awaitExit(t, c) })
			awaitReady(t, &output)
			if action == "stop" {
				c.Stop()
			} else {
				cancel()
			}
			e := awaitExit(t, c)
			if !e.Requested || e.Err != nil {
				t.Fatalf("exit: %+v", e)
			}
		})
	}
}

func TestBackoffConcurrent(_ *testing.T) {
	b := shortBackoff()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				b.Next()
				b.Current()
				b.Failures()
				b.Saturated()
				b.Stable(time.Second)
				b.Reset()
			}
		})
	}
	wg.Wait()
}

func TestHelperRelativeInvocationWithWorkingDirectory(t *testing.T) {
	original := os.Args[0]
	os.Args[0] = filepath.Join("relative", "unit.test")
	t.Cleanup(func() { os.Args[0] = original })
	spec := helperSpec("echo")
	spec.Dir = t.TempDir()
	var output observedOutput
	spec.Stdout = &output
	exit := awaitExit(t, startHelper(t, spec))
	if exit.Code != 0 || !strings.Contains(output.String(), spec.Dir) {
		t.Fatalf("relative invocation with Dir: %+v %q", exit, output.String())
	}
}
