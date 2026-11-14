//go:build linux

package procguard

import (
	"os/exec"
	"runtime"
)

func startProcess(cmd *exec.Cmd) (func(), error) {
	ready := make(chan error, 1)
	release, released := make(chan struct{}), make(chan struct{})
	go func() {
		// Linux ties Pdeathsig to the creating thread, not the parent process.
		// Keep that thread alive until the child has been reaped.
		runtime.LockOSThread()
		err := cmd.Start()
		ready <- err
		if err == nil {
			<-release
		}
		runtime.UnlockOSThread()
		close(released)
	}()
	if err := <-ready; err != nil {
		<-released
		return nil, err
	}
	return func() { close(release); <-released }, nil
}
