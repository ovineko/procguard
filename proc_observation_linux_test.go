//go:build linux

package procguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ovineko/procguard/internal/testsupport"
	"golang.org/x/sys/unix"
)

type procIdentity struct {
	PID   int    `json:"pid"`
	PGID  int    `json:"pgid"`
	SID   int    `json:"sid"`
	Error string `json:"error"`
}

type procObservationFixture struct {
	cmd                                   *exec.Cmd
	identity                              procIdentity
	migrationGate, migratedFile, exitGate string
	wait                                  func() error
}

func TestProcGroupObservationHelper(t *testing.T) {
	if !helperEnabled() {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) != 6 {
		os.Exit(90)
	}
	args = args[1:]
	target, err := strconv.Atoi(args[0])
	if err != nil {
		os.Exit(91)
	}
	publish := func(path string) {
		pgid, e1 := unix.Getpgid(0)
		sid, e2 := unix.Getsid(0)
		id := procIdentity{PID: os.Getpid(), PGID: pgid, SID: sid}
		if e1 != nil {
			id.Error = e1.Error()
		}
		if e2 != nil {
			id.Error = e2.Error()
		}
		data, e := json.Marshal(id)
		if e != nil {
			os.Exit(92)
		}
		tmp := path + ".tmp"
		if e = os.WriteFile(tmp, data, 0o600); e != nil {
			os.Exit(93)
		}
		if e = os.Rename(tmp, path); e != nil {
			os.Exit(94)
		}
	}
	publish(args[1])
	if target >= 0 {
		helperAwaitFile(args[2])
		if err := unix.Setpgid(0, target); err != nil {
			os.Exit(95)
		}
		publish(args[3])
	}
	helperAwaitFile(args[4])
	os.Exit(0)
}

func startProcObservationHelper(t *testing.T, initialGroup, targetGroup int) *procObservationFixture {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	migration, migrated, exit := filepath.Join(dir, "migration"), filepath.Join(dir, "migrated"), filepath.Join(dir, "exit")
	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	cmd := exec.CommandContext(ctx, testsupport.Executable(), "-test.run=^TestProcGroupObservationHelper$", "--", strconv.Itoa(targetGroup), ready, migration, migrated, exit)
	cmd.Env = helperEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: initialGroup}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	var waitErr error
	done := make(chan struct{})
	wait := func() error {
		once.Do(func() { go func() { waitErr = cmd.Wait(); close(done) }() })
		select {
		case <-done:
			return waitErr
		case <-time.After(testDeadline):
			return errors.New("helper wait timed out")
		}
	}
	f := &procObservationFixture{cmd: cmd, migrationGate: migration, migratedFile: migrated, exitGate: exit, wait: wait}
	t.Cleanup(func() { helperWriteFile(migration, "go"); helperWriteFile(exit, "exit"); cancel(); _ = f.wait() })
	awaitMarker(t, ready)
	data, err := os.ReadFile(ready)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f.identity); err != nil {
		t.Fatal(err)
	}
	if f.identity.Error != "" {
		t.Fatal(f.identity.Error)
	}
	return f
}

func readProcIdentity(t *testing.T, path string) procIdentity {
	t.Helper()
	awaitMarker(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var id procIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatal(err)
	}
	if id.Error != "" {
		t.Fatal(id.Error)
	}
	return id
}

func TestRemainingProcKernelTransitions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initialSame bool
		target      func(int) int
		disappear   bool
	}{
		{"leaves-watched-group", true, func(int) int { return 0 }, false},
		{"joins-watched-group", false, func(pid int) int { return pid }, false},
		{"disappears-before-lookup", true, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leader := startProcObservationHelper(t, 0, -1)
			leaderPID := leader.identity.PID
			if leader.identity.PGID != leaderPID {
				t.Fatalf("leader identity=%+v", leader.identity)
			}
			initial := 0
			if tc.initialSame {
				initial = leaderPID
			}
			target := -1
			if tc.target != nil {
				target = tc.target(leaderPID)
			}
			member := startProcObservationHelper(t, initial, target)
			if member.identity.SID != leader.identity.SID {
				t.Fatalf("initial SID mismatch: leader=%+v candidate=%+v", leader.identity, member.identity)
			}
			if tc.initialSame && member.identity.PGID != leaderPID {
				t.Fatalf("candidate not initially in watched group: %+v", member.identity)
			}
			if !tc.initialSame && member.identity.PGID == leaderPID {
				t.Fatalf("candidate unexpectedly in watched group: %+v", member.identity)
			}
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, strconv.Itoa(member.identity.PID)), 0o700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			lookedPGID := 0
			var lookupErr error
			readFile := func(path string) ([]byte, error) {
				if path != filepath.Join(root, strconv.Itoa(member.identity.PID), "stat") {
					t.Errorf("read path=%q", path)
				}
				if tc.disappear {
					helperWriteFile(member.exitGate, "exit")
					if err := member.wait(); err != nil {
						t.Errorf("candidate exit: %v", err)
					}
				} else {
					helperWriteFile(member.migrationGate, "go")
					after := readProcIdentity(t, member.migratedFile)
					if after.PID != member.identity.PID || after.SID != member.identity.SID {
						t.Errorf("identity transition initial=%+v after=%+v", member.identity, after)
					}
					if tc.name == "leaves-watched-group" && after.PGID != after.PID {
						t.Errorf("migrated PGID=%d want PID=%d", after.PGID, after.PID)
					}
					if tc.name == "joins-watched-group" && after.PGID != leaderPID {
						t.Errorf("migrated PGID=%d want %d", after.PGID, leaderPID)
					}
				}
				return []byte(fmt.Sprintf("%d (candidate) X 0 -1 -1", member.identity.PID)), nil
			}
			remaining, err := remainingInProc(root, leaderPID, readFile, func(pid int) (int, error) {
				calls++
				if pid != member.identity.PID {
					t.Errorf("lookup PID=%d want=%d", pid, member.identity.PID)
				}
				lookedPGID, lookupErr = unix.Getpgid(pid)
				return lookedPGID, lookupErr
			})
			if calls != 1 {
				t.Fatalf("lookup calls=%d", calls)
			}
			if tc.disappear {
				if !errors.Is(lookupErr, unix.ESRCH) || remaining || err != nil {
					t.Fatalf("lookup pgid=%d err=%v remaining=%v scanner=%v", lookedPGID, lookupErr, remaining, err)
				}
				if reaped, e := leaderReaped(member.identity.PID); e != nil || !reaped {
					t.Fatalf("reaped=%v err=%v", reaped, e)
				}
				return
			}
			if lookupErr != nil {
				t.Fatalf("real lookup: %v", lookupErr)
			}
			if tc.name == "leaves-watched-group" {
				if lookedPGID == leaderPID || remaining || err != nil {
					t.Fatalf("pgid=%d remaining=%v err=%v", lookedPGID, remaining, err)
				}
			} else if lookedPGID != leaderPID || err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("pgid=%d remaining=%v err=%v", lookedPGID, remaining, err)
			}
		})
	}
}
