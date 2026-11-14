//go:build windows

package procguard

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func processGone(_ *testing.T, pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	state, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && state == windows.WAIT_OBJECT_0
}

func TestWindowsExtraFiles(t *testing.T) {
	spec := helperSpec("wait")
	spec.ExtraFiles = []*os.File{os.Stdin}
	if child, err := Start(t.Context(), spec); child != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("start: %v %v", child, err)
	}
}

func TestWindowsGrace(t *testing.T) {
	var out observedOutput
	spec := helperSpec("wait")
	spec.Stdout = &out
	child := startHelper(t, spec)
	awaitReady(t, &out)
	child.Stop()
	exit := awaitExit(t, child)
	if !exit.Killed || !exit.Requested || exit.Signal != nil || exit.Err != nil {
		t.Fatalf("exit: %+v", exit)
	}
}
