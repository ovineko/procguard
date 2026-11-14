//go:build !linux && !windows

package procguard

import (
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd, _ *cgroupResources) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
