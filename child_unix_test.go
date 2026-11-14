//go:build !windows

package procguard

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestUnixTermination(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		signal     bool
		code       int
		killed     bool
	}{
		{"signal", "wait", true, -1, false}, {"graceful", "graceful", false, 0, false}, {"escalation", "ignore", false, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out observedOutput
			spec := helperSpec(tc.mode)
			spec.Stdout = &out
			c := startHelper(t, spec)
			awaitReady(t, &out)
			stopped := time.Now()
			if tc.signal {
				if err := c.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else {
				c.Stop()
			}
			e := awaitExit(t, c)
			if !treeEnumerationAvailable() && !tc.signal {
				tc.killed = true
			}
			if e.Code != tc.code || e.Killed != tc.killed || e.Requested == tc.signal || e.Err != nil {
				t.Fatalf("exit: %+v", e)
			}
			if tc.code == -1 && e.Signal == nil {
				t.Fatal("signal absent")
			}
			if tc.killed && time.Since(stopped) < spec.GracePeriod {
				t.Fatal("early escalation")
			}
		})
	}
}

func TestExtraFiles(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	spec := helperSpec("fd")
	spec.ExtraFiles = []*os.File{writer}
	spec.AfterStart = func(_ int, _ error) {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}
	c := startHelper(t, spec)
	if e := awaitExit(t, c); e.Code != 0 {
		t.Fatalf("exit: %+v", e)
	}
	data := make([]byte, 64)
	n, err := reader.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:n]) != "inherited-data" {
		t.Fatalf("data: %q", data[:n])
	}
}

func TestNaturalExitWithoutDescendantsIsPrompt(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.GracePeriod = testScale(10 * time.Second)
	started := time.Now()
	exit := awaitExit(t, startHelper(t, spec))
	if exit.Err != nil || exit.Killed || time.Since(started) >= testScale(time.Second) {
		t.Fatalf("exit/duration: %+v %v", exit, time.Since(started))
	}
}
