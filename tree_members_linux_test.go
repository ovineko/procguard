//go:build linux

package procguard

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

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
