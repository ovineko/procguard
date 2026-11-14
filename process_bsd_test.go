//go:build !linux && !windows

package procguard

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func processGone(t *testing.T, pid int) bool {
	t.Helper()
	data, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(data))
	gone, probeErr := classifyPS(state, err)
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	return gone
}

func classifyPS(state string, err error) (bool, error) {
	var exit *exec.ExitError
	if err != nil {
		if errors.As(err, &exit) && exit.ExitCode() == 1 && state == "" && len(exit.Stderr) == 0 {
			return true, nil
		}
		return false, fmt.Errorf("probe process with ps: %w", err)
	}
	return state == "" || strings.HasPrefix(state, "Z"), nil
}

func TestPSFailureIsNotGone(t *testing.T) {
	if gone, err := classifyPS("", exec.ErrNotFound); err == nil || gone {
		t.Fatal("missing ps treated as gone")
	}
	if gone, err := classifyPS("Z", nil); err != nil || !gone {
		t.Fatal("zombie not recognized")
	}
}

func TestPSExitClassification(t *testing.T) {
	for _, code := range []string{"1", "2"} {
		spec := helperSpec("exit", code)
		// #nosec G204 -- Re-executes the local test binary to obtain a real exit status.
		cmd := exec.Command(spec.Path, spec.Args...)
		cmd.Env = spec.Env
		_, err := cmd.Output()
		var status *exec.ExitError
		if !errors.As(err, &status) {
			t.Fatalf("helper status: %v", err)
		}
		gone, probeErr := classifyPS("", status)
		if code == "1" {
			if !gone || probeErr != nil {
				t.Fatalf("missing PID: %v %v", gone, probeErr)
			}
		} else if gone || probeErr == nil {
			t.Fatal("ps failure accepted")
		}
		status.Stderr = []byte("ps: system query failed")
		if gone, err := classifyPS("", status); gone || err == nil {
			t.Fatal("ps diagnostic ignored")
		}
	}
}
