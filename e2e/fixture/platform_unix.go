//go:build e2e && !windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
	"golang.org/x/sys/unix"
)

func termSignals(mode string) (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(ch, syscall.SIGTERM)
	}
	return ch, func() { signal.Stop(ch) }
}

func escapeSession() error {
	if _, err := syscall.Setsid(); err != nil {
		return fmt.Errorf("setsid: %w", err)
	}
	return nil
}

func hardKill(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("kill: %w", err)
	}
	return nil
}

func extraFile() error {
	file := os.NewFile(3, "inherited")
	if file == nil {
		return errors.New("missing inherited descriptor")
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, len(wire.ExtraPayload))
	if _, err := io.ReadFull(file, data); err != nil {
		return fmt.Errorf("read descriptor 3: %w", err)
	}
	if string(data) != wire.ExtraPayload {
		return fmt.Errorf("descriptor 3 payload mismatch: %q", data)
	}
	if _, err := file.WriteAt([]byte(wire.ExtraAck), 0); err != nil {
		return fmt.Errorf("acknowledge descriptor 3: %w", err)
	}
	return nil
}

func sessionIdentity() (wire.Record, error) {
	pgid, err := unix.Getpgid(0)
	if err != nil {
		return wire.Record{}, fmt.Errorf("get pgid: %w", err)
	}
	sid, err := unix.Getsid(0)
	if err != nil {
		return wire.Record{}, fmt.Errorf("get sid: %w", err)
	}
	return wire.Record{PGID: pgid, SID: sid}, nil
}
