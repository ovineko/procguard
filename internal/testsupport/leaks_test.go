package testsupport

import (
	"strings"
	"testing"
	"time"
)

func TestLeakDetection(t *testing.T) {
	before := Stacks()
	release, started, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { defer close(done); close(started); <-release }()
	<-started
	err := waitNoLeaks(before, Scale(time.Millisecond))
	close(release)
	<-done
	if err == nil || !strings.Contains(err.Error(), "TestLeakDetection.func") {
		t.Fatalf("missing leaked stack: %v", err)
	}
	if err := WaitNoLeaks(before); err != nil {
		t.Fatal(err)
	}
}
