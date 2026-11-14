//go:build e2e && windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/ovineko/procguard"
	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"golang.org/x/sys/windows"
)

func termSignals(_ string) (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch) }
}
func escapeSession() error { return nil }
func hardKill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}
	defer func() { _ = p.Release() }()
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill process: %w", err)
	}
	return nil
}

func processState(pid int) (bool, bool, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // PIDs originate from the OS.
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("open process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, false, fmt.Errorf("wait process: %w", err)
	}
	return state != windows.WAIT_OBJECT_0, false, nil
}
func extraFile() error { return procguard.ErrUnsupported }

func sessionIdentity() (wire.Record, error) { return wire.Record{}, nil }
