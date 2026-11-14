package procguard

import (
	"errors"
	"fmt"
)

// CleanupState reports whether termination-unit cleanup was proven complete.
type CleanupState uint8

const (
	// CleanupUnconfirmed is the zero value and never authorizes another launch.
	CleanupUnconfirmed CleanupState = iota
	// CleanupConfirmed means required process-unit and leader cleanup evidence exists.
	CleanupConfirmed
)

// ErrCleanupUnconfirmed marks a termination unit whose cleanup was not proven complete.
var ErrCleanupUnconfirmed = errors.New("process cleanup unconfirmed")

// StartError retains launch failure details and whether a created unit was confirmed clean.
type StartError struct {
	PID     int
	Cleanup CleanupState
	Err     error
}

func (e *StartError) Error() string {
	if e == nil {
		return "process start failed"
	}
	return fmt.Sprintf("process start failed (pid %d, cleanup %d): %v", e.PID, e.Cleanup, e.Err)
}

func (e *StartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newStartError(pid int, cleanup CleanupState, err error) error {
	if err == nil {
		return nil
	}
	if cleanup != CleanupConfirmed && !errors.Is(err, ErrCleanupUnconfirmed) {
		err = errors.Join(err, ErrCleanupUnconfirmed)
	}
	return &StartError{PID: pid, Cleanup: cleanup, Err: err}
}
