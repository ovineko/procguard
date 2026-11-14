//go:build linux

package procguard

import (
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd, res *cgroupResources) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	if res != nil {
		res.attach(cmd)
	}
}
