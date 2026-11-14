//go:build e2e_docker

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ovineko/procguard/e2e"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"github.com/ovineko/procguard/internal/testsupport"
)

func TestDelegatedCgroup(t *testing.T) {
	requireDocker(t)
	// clone3 is blocked by Docker's default seccomp profile. Host cgroup namespace
	// paths keep /proc membership comparable; only our delegated subtree is writable.
	parent := delegatedCgroup(t)
	binary := e2e.BuildFixture(t, "../..", "linux", runtime.GOARCH)
	p := profile{
		name: "delegated-cgroup", image: "debian:bookworm-slim", arch: runtime.GOARCH, class: e2e.Linux,
		flags: []string{
			"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges",
			"--security-opt", "seccomp=unconfined", "--cgroupns", "host",
			"--mount", "type=bind,src=" + parent + ",dst=" + parent,
			"--env", wire.CgroupParentEnv + "=" + parent,
		},
	}
	s := e2e.ScenarioByID("cgroup-v2")
	e2e.Assert(t, s, e2e.Linux, "linux", run(t, p, binary, s.Mode, "-scenario", s.ID, "-dir", "/tmp"))
}

func delegatedCgroup(t *testing.T) string {
	t.Helper()
	parent := wire.CgroupParent()
	if parent == "" {
		unit := fmt.Sprintf("procguard-e2e-%d-%d.service", os.Getpid(), time.Now().UnixNano())
		userSystemd(t, "systemd-run", "--user", "--quiet", "--collect", "--unit="+unit,
			"--property=Delegate=yes", "--property=DelegateSubgroup=supervisor", "--property=Type=exec", "sleep", "infinity")
		t.Cleanup(func() { userSystemd(t, "systemctl", "--user", "stop", unit) })
		group := strings.TrimSpace(userSystemd(t, "systemctl", "--user", "show", "--property=ControlGroup", "--value", unit))
		if !strings.HasPrefix(group, "/") || group == "/" {
			t.Fatalf("invalid delegated unit cgroup: %q", group)
		}
		parent = filepath.Join("/sys/fs/cgroup", group)
		if err := os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+memory\n"), 0o600); err != nil {
			t.Fatalf("enable memory in owned delegated unit: %v", err)
		}
	}
	if !filepath.IsAbs(parent) {
		t.Fatalf("%s must name an absolute delegated cgroup directory", wire.CgroupParentEnv)
	}
	path := filepath.Join(parent, fmt.Sprintf("procguard-docker-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatalf("create isolated test subtree in %s: %v", parent, err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove delegated test subtree: %v", err)
		}
	})
	return path
}

func userSystemd(t *testing.T, command string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testsupport.Scale(10*time.Second))
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s; provide %s if no delegated systemd user manager is available", command, args, err, out, wire.CgroupParentEnv)
	}
	return string(out)
}
