//go:build !windows

package procguard

import (
	"os"
	"os/signal"
	"syscall"
)

func helperSignals(mode string) <-chan os.Signal {
	signals := make(chan os.Signal, 1)
	if mode == "graceful" {
		signal.Notify(signals, syscall.SIGTERM)
	}
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	// A timer-backed goroutine prevents the helper's intentional wait from being
	// diagnosed as a Go runtime deadlock when no signals are registered.
	go func() { select {} }()
	signal.Notify(make(chan os.Signal, 1), syscall.SIGUSR2)
	return signals
}
