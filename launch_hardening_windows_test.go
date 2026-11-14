//go:build windows

package procguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"unsafe"

	"github.com/ovineko/procguard/internal/testsupport"
	"golang.org/x/sys/windows"
)

const windowsLaunchHelperTest = "^TestWindowsLaunchHelper$"

func processInJob(process, job windows.Handle) (bool, error) {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	if err := proc.Find(); err != nil {
		return false, err
	}
	var inJob int32
	result, _, err := proc.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&inJob)))
	if result == 0 {
		return false, err
	}
	return inJob != 0, nil
}

func TestWindowsLaunchHelper(t *testing.T) {
	if !helperEnabled() {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 || args[1] != "launch" {
		return
	}
	args = args[2:]
	if len(args) != 3 {
		os.Exit(99)
	}
	jobValue, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		os.Exit(98)
	}
	ok, err := processInJob(windows.CurrentProcess(), windows.Handle(jobValue))
	if err != nil || !ok {
		os.Exit(97)
	}
	if err := os.WriteFile(args[1], []byte("membership-checked"), 0o600); err != nil {
		os.Exit(96)
	}
	if err := os.WriteFile(args[2], []byte("executed"), 0o600); err != nil {
		os.Exit(95)
	}
	os.Exit(0)
}

func launchHelperSpec(job string, checked, marker string) Spec {
	return Spec{Path: testsupport.Executable(), Args: []string{"-test.run=" + windowsLaunchHelperTest, "--", "launch", job, checked, marker}, Env: helperEnv()}
}

func TestWindowsLaunchAssignedBeforeResume(t *testing.T) {
	dir := t.TempDir()
	checked, marker := filepath.Join(dir, "membership"), filepath.Join(dir, "executed")
	spec := launchHelperSpec("0", checked, marker)
	var job windows.Handle
	var helperPID int
	child, err := startWith(t.Context(), spec, launchHooks{
		newTree: func(pid int, res *cgroupResources) (*processTree, error) {
			helperPID = pid
			tree, err := newProcessTree(pid, res)
			if err == nil {
				job = tree.job
			}
			return tree, err
		},
		beforeResume: func(tree *processTree) error {
			if tree.job != job || job == 0 {
				return errors.New("job handle was not retained")
			}
			process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(helperPID))
			if err != nil {
				return fmt.Errorf("open suspended child: %w", err)
			}
			defer windows.CloseHandle(process)
			ok, err := processInJob(process, job)
			if err != nil || !ok {
				return fmt.Errorf("child not in assigned job: ok=%v err=%v", ok, err)
			}
			if _, err := os.Stat(checked); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("first instruction ran before resume: %v", err)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	exit := awaitExit(t, child)
	if exit.Err != nil || exit.Code != 0 {
		t.Fatalf("child exit: %+v", exit)
	}
	if _, err := os.Stat(checked); err != nil {
		t.Fatalf("first-instruction membership check did not run: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "executed" {
		t.Fatalf("execution marker: %q, %v", data, err)
	}
}

func TestWindowsLaunchFailureDoesNotExecute(t *testing.T) {
	for _, tc := range []struct {
		name string
	}{
		{name: "tree-setup"},
		{name: "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			checked, marker := filepath.Join(dir, "membership"), filepath.Join(dir, "executed")
			spec := launchHelperSpec("0", checked, marker)
			var calls, callbackPID int
			var callbackErr error
			spec.AfterStart = func(pid int, err error) { calls++; callbackPID, callbackErr = pid, err }
			wantErr := errors.New("injected " + tc.name + " failure")
			hooks := launchHooks{
				newTree: func(pid int, res *cgroupResources) (*processTree, error) {
					callbackPID = pid
					if tc.name == "tree-setup" {
						return nil, wantErr
					}
					return newProcessTree(pid, res)
				},
			}
			if tc.name == "resume" {
				hooks.resume = func(*processTree) error { return wantErr }
			}
			_, err := startWith(context.Background(), spec, hooks)
			if !errors.Is(err, wantErr) {
				t.Fatalf("start error %v does not include %v", err, wantErr)
			}
			if calls != 1 || callbackPID == 0 || callbackErr == nil || !errors.Is(callbackErr, wantErr) {
				t.Fatalf("AfterStart calls=%d pid=%d err=%v", calls, callbackPID, callbackErr)
			}
			if _, err := os.Stat(checked); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("first instruction ran despite launch failure: stat=%v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("child executed despite launch failure: stat=%v", err)
			}
			if !processGone(t, callbackPID) {
				t.Fatalf("failed launch process %d survived", callbackPID)
			}
		})
	}
}

func TestWindowsCancelBeforeResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	checked, marker := filepath.Join(dir, "membership"), filepath.Join(dir, "executed")
	spec := launchHelperSpec("0", checked, marker)
	var callbackPID int
	var callbackErr error
	spec.AfterStart = func(pid int, err error) { callbackPID, callbackErr = pid, err }
	_, err := startWith(ctx, spec, launchHooks{
		beforeResume: func(*processTree) error {
			cancel()
			return nil
		},
		resume: func(*processTree) error {
			t.Fatal("cancelled child must not be resumed")
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error %v, want context cancellation", err)
	}
	if callbackPID == 0 || callbackErr == nil || !errors.Is(callbackErr, context.Canceled) {
		t.Fatalf("AfterStart pid=%d err=%v", callbackPID, callbackErr)
	}
	if _, err := os.Stat(checked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first instruction ran before cancellation: stat=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child executed before cancellation: stat=%v", err)
	}
	if !processGone(t, callbackPID) {
		t.Fatalf("cancelled launch process %d survived", callbackPID)
	}
}

func TestWindowsCancelBeforeSpawn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	spec := launchHelperSpec("0", filepath.Join(dir, "membership"), filepath.Join(dir, "executed"))
	var calls, callbackPID int
	var callbackErr error
	spec.AfterStart = func(pid int, err error) {
		calls++
		callbackPID, callbackErr = pid, err
	}
	child, err := Start(ctx, spec)
	var startErr *StartError
	if child != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &startErr) {
		t.Fatalf("pre-spawn cancellation child=%v err=%v", child, err)
	}
	if startErr.PID != 0 || startErr.Cleanup != CleanupConfirmed || calls != 1 || callbackPID != 0 || !errors.Is(callbackErr, context.Canceled) {
		t.Fatalf("pre-spawn evidence=%+v callback calls=%d pid=%d err=%v", startErr, calls, callbackPID, callbackErr)
	}
}
