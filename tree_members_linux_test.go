//go:build linux

package procguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const capturedExitingProcStat = "6082 (true) X 0 -1 -1 0 -1 4227084 32 0 0 0 0 0 0 0 20 0 0 0 5018816 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 17 3 0 0 0 0 0 0 0 0 0 0 0 0 0\n"

func TestParseProcessStat(t *testing.T) {
	for _, state := range []byte{'S', 'Z', 'X'} {
		group, got, err := parseProcessStat("123 (name ) ( with spaces)) " + string(state) + " 1 456 789 0")
		if err != nil || group != 456 || got != state {
			t.Fatalf("stat: %d %c %v", group, got, err)
		}
	}
	for _, stat := range []string{"123 missing", "123 (name)", "123 (name) S 1", "123 (name) S 1 invalid"} {
		if _, _, err := parseProcessStat(stat); err == nil {
			t.Fatalf("accepted malformed stat: %q", stat)
		}
	}
}

func TestRemainingProcFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readErr  error
		group    int
		groupErr error
		wantErr  bool
	}{
		{"vanished", os.ErrNotExist, 0, nil, false},
		{"esrch", unix.ESRCH, 0, nil, false},
		{"unrelated-unreadable", os.ErrPermission, 42, nil, false},
		{"unreadable-vanished", os.ErrPermission, 0, unix.ESRCH, false},
		{"own-unreadable", os.ErrPermission, 123, nil, true},
		{"unknown-group", os.ErrPermission, 0, unix.EPERM, true},
		{"io-error", unix.EIO, 0, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "456"), 0o700); err != nil {
				t.Fatal(err)
			}
			live, err := remainingInProc(root, 123, func(string) ([]byte, error) { return nil, tc.readErr }, func(int) (int, error) { return tc.group, tc.groupErr })
			if live || (err != nil) != tc.wantErr {
				t.Fatalf("live=%v err=%v", live, err)
			}
		})
	}
	if _, err := remainingInProc(filepath.Join(t.TempDir(), "absent"), 123, os.ReadFile, unix.Getpgid); err == nil {
		t.Fatal("unreadable proc root accepted")
	}
}

func TestRemainingProcMembers(t *testing.T) {
	root := t.TempDir()
	for _, record := range []struct{ pid, stat string }{
		{"123", "123 (leader) S 1 123"},
		{"456", "456 (zombie) Z 1 123"},
		{"789", "789 (unrelated) S 1 789"},
	} {
		dir := filepath.Join(root, record.pid)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(record.stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if live, err := remainingInProc(root, 123, os.ReadFile, unix.Getpgid); err != nil || live {
		t.Fatalf("empty group: live=%v err=%v", live, err)
	}
	if err := os.WriteFile(filepath.Join(root, "456", "stat"), []byte("456 (live) S 1 123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if live, err := remainingInProc(root, 123, os.ReadFile, unix.Getpgid); err != nil || !live {
		t.Fatalf("live group: live=%v err=%v", live, err)
	}
}

func TestRemainingProcMalformedMembership(t *testing.T) {
	const leader, scanned = 5835, 6082
	for _, record := range []struct {
		name string
		raw  string
	}{
		{"captured-exiting", capturedExitingProcStat},
		{"sampled-live", strings.Replace(capturedExitingProcStat, ") X ", ") S ", 1)},
		{"empty", ""},
	} {
		t.Run(record.name, func(t *testing.T) {
			_, _, parseErr := parseProcessStat(record.raw)
			if parseErr == nil {
				t.Fatal("strict parser accepted the malformed record")
			}
			for _, tc := range []struct {
				name     string
				group    int
				groupErr error
				wantErr  bool
			}{
				{"vanished", 0, unix.ESRCH, false},
				{"wrapped-vanished", 0, fmt.Errorf("lookup: %w", unix.ESRCH), false},
				{"unrelated", 42, nil, false},
				{"zero-group", 0, nil, false},
				{"owned", leader, nil, true},
				{"negative-group", -1, nil, true},
				{"permission-denied", 0, unix.EPERM, true},
				{"io-error", 0, unix.EIO, true},
				{"interrupted", 0, unix.EINTR, true},
				{"not-exist-is-not-esrch", 0, os.ErrNotExist, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					if err := os.Mkdir(filepath.Join(root, "6082"), 0o700); err != nil {
						t.Fatal(err)
					}
					live, err := remainingInProc(root, leader, func(string) ([]byte, error) {
						return []byte(record.raw), nil
					}, func(pid int) (int, error) {
						if pid != scanned {
							t.Fatalf("lookup PID=%d, want scanned PID=%d", pid, scanned)
						}
						return tc.group, tc.groupErr
					})
					if live || (err != nil) != tc.wantErr {
						t.Fatalf("live=%v err=%v, want error=%v", live, err, tc.wantErr)
					}
					if tc.wantErr {
						if !strings.Contains(err.Error(), parseErr.Error()) {
							t.Fatalf("parser diagnostic lost: %v", err)
						}
						if tc.groupErr != nil && !errors.Is(err, tc.groupErr) {
							t.Fatalf("lookup error identity lost: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestRemainingProcContinuesAfterMalformedUnrelated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group int
		err   error
	}{
		{"vanished", 0, unix.ESRCH},
		{"unrelated", 42, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, record := range []struct{ pid, stat string }{
				{"100", "100 (gone) X 0 -1 -1"},
				{"200", "200 (member) S 1 5835"},
			} {
				dir := filepath.Join(root, record.pid)
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(record.stat), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			live, err := remainingInProc(root, 5835, os.ReadFile, func(pid int) (int, error) {
				if pid != 100 {
					t.Fatalf("unexpected lookup for valid record PID %d", pid)
				}
				return tc.group, tc.err
			})
			if !live || err != nil {
				t.Fatalf("later live group member missed: live=%v err=%v", live, err)
			}
		})
	}
}

func TestRemainingProcValidRecordsNeedNoLookup(t *testing.T) {
	for _, tc := range []struct {
		name string
		stat string
		live bool
	}{
		{"unrelated", "6082 (other) S 1 42", false},
		{"owned-live", "6082 (member) S 1 5835", true},
		{"owned-zombie", "6082 (member) Z 1 5835", false},
		{"owned-dead", "6082 (member) X 1 5835", false},
		{"owned-dead-lowercase", "6082 (member) x 1 5835", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "6082"), 0o700); err != nil {
				t.Fatal(err)
			}
			live, err := remainingInProc(root, 5835, func(string) ([]byte, error) {
				return []byte(tc.stat), nil
			}, func(pid int) (int, error) {
				t.Fatalf("valid stat unexpectedly triggered lookup for PID %d", pid)
				return 0, unix.EPERM
			})
			if live != tc.live || err != nil {
				t.Fatalf("live=%v err=%v, want live=%v", live, err, tc.live)
			}
		})
	}
}

type procStatCleanupProbe struct {
	cleanupTree

	probe func() (bool, error)
}

func (p *procStatCleanupProbe) remaining() (bool, error) { return p.probe() }

func TestChildExitWithProcStatRace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lookupErr error
		resolve   bool
		immediate bool
		wantState CleanupState
	}{
		{"vanished", unix.ESRCH, false, false, CleanupConfirmed},
		{"unrelated", nil, false, false, CleanupConfirmed},
		{"unknown-then-resolved", unix.EPERM, true, false, CleanupConfirmed},
		{"persistently-unknown", unix.EPERM, false, true, CleanupUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			spec := helperSpec("exit", "0")
			spec.KillImmediately = tc.immediate
			child, err := startWith(t.Context(), spec, launchHooks{
				cleanup: func(tree cleanupTree) cleanupTree {
					realTree, ok := tree.(*processTree)
					if !ok {
						t.Errorf("unexpected cleanup tree %T", tree)
						return tree
					}
					scanned := 6082
					if realTree.pid == scanned {
						scanned = 6083
					}
					if err := os.Mkdir(filepath.Join(root, strconv.Itoa(scanned)), 0o700); err != nil {
						t.Error(err)
						return tree
					}
					stat := capturedExitingProcStat
					if scanned != 6082 {
						stat = strings.Replace(stat, "6082 ", "6083 ", 1)
					}
					probes := 0
					return &procStatCleanupProbe{cleanupTree: tree, probe: func() (bool, error) {
						probes++
						if tc.resolve && probes > 1 {
							return realTree.remaining()
						}
						return remainingInProc(root, realTree.pid, func(string) ([]byte, error) {
							return []byte(stat), nil
						}, func(pid int) (int, error) {
							if pid != scanned {
								t.Errorf("lookup PID=%d, want scanned PID=%d", pid, scanned)
							}
							return realTree.pid + 1, tc.lookupErr
						})
					}}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { child.Stop(); awaitExit(t, child) })
			exit := awaitExit(t, child)
			if exit.Code != 0 || exit.Cleanup != tc.wantState {
				t.Fatalf("exit=%+v, want code 0 and cleanup %v", exit, tc.wantState)
			}
			if errors.Is(tc.lookupErr, unix.EPERM) {
				if !errors.Is(exit.Err, unix.EPERM) {
					t.Fatalf("membership uncertainty lost: %+v", exit)
				}
			} else if exit.Err != nil || exit.Signal != nil || exit.Requested || exit.Killed {
				t.Fatalf("unrelated proc entry poisoned successful exit: %+v", exit)
			}
			if tc.wantState == CleanupUnconfirmed && !errors.Is(exit.Err, ErrCleanupUnconfirmed) {
				t.Fatalf("unconfirmed cleanup marker missing: %+v", exit)
			}
			reaped, err := leaderReaped(child.PID())
			if err != nil || !reaped {
				t.Fatalf("leader %d reaped=%v err=%v", child.PID(), reaped, err)
			}
		})
	}
}
