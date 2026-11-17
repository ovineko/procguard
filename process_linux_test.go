//go:build linux

package procguard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func leaderReaped(pid int) (bool, error) {
	for {
		info := &unix.Siginfo{}
		err := unix.Waitid(unix.P_PID, pid, info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	}
}

func TestLeaderReapedDistinguishesZombie(t *testing.T) {
	dir := t.TempDir()
	ready, gate := filepath.Join(dir, "ready"), filepath.Join(dir, "exit")
	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	spec := helperSpec("gated-exit", ready, gate, "0")
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Env = spec.Env
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	var waitErr error
	waitDone := make(chan struct{})
	wait := func() error {
		once.Do(func() {
			go func() {
				waitErr = cmd.Wait()
				close(waitDone)
			}()
		})
		select {
		case <-waitDone:
			return waitErr
		case <-time.After(testDeadline):
			return errors.New("owned helper wait timed out")
		}
	}
	t.Cleanup(func() {
		helperWriteFile(gate, "exit")
		if err := wait(); err != nil {
			cancel()
			_ = wait()
		}
	})
	awaitMarker(t, ready)
	pid := cmd.Process.Pid
	if reaped, err := leaderReaped(pid); err != nil || reaped {
		t.Fatalf("live child reaped=%v err=%v", reaped, err)
	}
	helperWriteFile(gate, "exit")
	observed := make(chan error, 1)
	go func() {
		for {
			info := &unix.Siginfo{}
			err := unix.Waitid(unix.P_PID, pid, info, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			observed <- err
			return
		}
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testDeadline):
		cancel()
		if err := <-observed; err != nil {
			t.Fatalf("exit observation after cancellation: %v", err)
		}
		t.Fatal("exit observation timed out")
	}
	if !processGone(t, pid) {
		t.Fatalf("child %d not observed exited", pid)
	}
	if reaped, err := leaderReaped(pid); err != nil || reaped {
		t.Fatalf("zombie reaped=%v err=%v", reaped, err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if reaped, err := leaderReaped(pid); err != nil || !reaped {
		t.Fatalf("after Wait reaped=%v err=%v", reaped, err)
	}
}

func TestChildWaitReapsOwnedLeader(t *testing.T) {
	dir := t.TempDir()
	ready, gate := filepath.Join(dir, "ready"), filepath.Join(dir, "exit")
	barrier := &reapingBarrierTree{entered: make(chan struct{}), release: make(chan struct{})}
	child, err := startWith(t.Context(), helperSpec("gated-exit", ready, gate, "0"), launchHooks{
		cleanup: func(underlying cleanupTree) cleanupTree {
			barrier.cleanupTree = underlying
			return barrier
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	t.Cleanup(func() {
		helperWriteFile(gate, "exit")
		release()
		child.Stop()
		awaitExit(t, child)
	})
	awaitMarker(t, ready)
	helperWriteFile(gate, "exit")
	select {
	case <-barrier.entered:
	case <-time.After(testDeadline):
		t.Fatal("membership barrier not entered")
	}
	if reaped, err := leaderReaped(child.PID()); err != nil || reaped {
		t.Fatalf("before wait reaped=%v err=%v", reaped, err)
	}
	info := &unix.Siginfo{}
	if err := unix.Waitid(unix.P_PID, child.PID(), info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatal(err)
	}
	release()
	exit := awaitExit(t, child)
	if exit.Code != 0 || exit.Signal != nil || exit.Err != nil || exit.Requested || exit.Cleanup != CleanupConfirmed {
		t.Fatalf("unexpected lifecycle result: %+v", exit)
	}
	if reaped, err := leaderReaped(child.PID()); err != nil || !reaped {
		t.Fatalf("after Child.Wait reaped=%v err=%v", reaped, err)
	}
}

func processGone(_ *testing.T, pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+1:])
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}
