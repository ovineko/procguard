//go:build !linux

package procguard

import "os/exec"

//nolint:wrapcheck // Start wraps this error consistently after platform launch setup.
func startProcess(cmd *exec.Cmd) (func(), error) {
	return func() {}, cmd.Start()
}
