package procguard

import (
	"fmt"
	"os"
	"testing"

	"github.com/ovineko/procguard/internal/testsupport"
)

func TestMain(m *testing.M) {
	before := testsupport.Stacks()
	code := m.Run()
	if !helperEnabled() {
		if err := testsupport.WaitNoLeaks(before); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}
