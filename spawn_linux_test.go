//go:build linux

package procguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSpawningThreadLifetime(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(strconv.FormatBool(guarded), func(t *testing.T) {
			ready := readyPath(t)
			spec := helperSpec("graceful", ready)
			thread := make(chan int, 1)
			var child *Child
			var cmd *exec.Cmd
			var startErr error
			releaseMain := make(chan struct{})
			t.Cleanup(func() { close(releaseMain) })
			var launch func()
			launch = func() {
				runtime.LockOSThread()
				if unix.Gettid() == os.Getpid() {
					// The group leader can remain present until the whole process exits.
					go launch()
					<-releaseMain
					runtime.UnlockOSThread()
					return
				}
				if guarded {
					child, startErr = Start(t.Context(), spec)
				} else {
					// #nosec G204 -- Re-executes the test binary with fixed helper arguments.
					cmd = exec.Command(spec.Path, spec.Args...)
					cmd.Env = spec.Env
					setProcAttr(cmd, nil)
					startErr = cmd.Start()
				}
				if startErr == nil {
					helperAwaitFile(ready)
				}
				// Returning locked deliberately destroys this thread.
				thread <- unix.Gettid()
			}
			go launch()
			var tid int
			select {
			case tid = <-thread:
			case <-time.After(testDeadline):
				t.Fatal("spawn thread timed out")
			}
			if startErr != nil {
				t.Fatal(startErr)
			}
			if guarded {
				eventually(t, func() bool {
					_, err := os.Stat(filepath.Join("/proc/self/task", strconv.Itoa(tid)))
					return os.IsNotExist(err)
				})
				t.Cleanup(func() { child.Stop(); awaitExit(t, child) })
				select {
				case <-child.Done():
					t.Fatalf("caller thread exit killed child: %+v", child.Wait())
				case <-time.After(testScale(100 * time.Millisecond)):
				}
			} else {
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				t.Cleanup(func() { _ = cmd.Process.Kill() })
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(testDeadline):
					data, _ := os.ReadFile(filepath.Join("/proc/self/task", strconv.Itoa(tid), "stat"))
					t.Fatalf("raw spawn did not receive parent-thread SIGTERM; spawning task: %s", data)
				}
			}
		})
	}
}
