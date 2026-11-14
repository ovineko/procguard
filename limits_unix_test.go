//go:build !windows && !openbsd

package procguard

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"github.com/ovineko/procguard/internal/testsupport"
)

func helperSelfLimits(args []string) {
	request, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		os.Exit(98)
	}
	var before, after syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_AS, &before) != nil {
		os.Exit(98)
	}
	if ApplyLimitsToSelf(Limits{AddressSpace: request}) != nil {
		os.Exit(98)
	}
	observeErr := syscall.Getrlimit(syscall.RLIMIT_AS, &after)
	// Coverage flushing at os.Exit can allocate beyond the lowered limit. Restore
	// only this helper's soft limit after capturing the actual kernel value.
	restoreErr := syscall.Setrlimit(syscall.RLIMIT_AS, &before)
	if observeErr != nil || restoreErr != nil {
		os.Exit(98)
	}
	if after.Cur != clampToHardLimit(request, before.Max) || after.Max != before.Max {
		os.Exit(98)
	}
	os.Exit(0)
}

func TestSelfAddressSpace(t *testing.T) {
	testsupport.SkipAddressSpace(t)
	if raceEnabled {
		t.Skip("RLIMIT_AS conflicts with ThreadSanitizer memory needs")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Darwin rejects lowering RLIMIT_AS with EINVAL")
	}
	for _, request := range []int64{512 << 20, 256 << 20, 64 << 20, 1 << 62} {
		t.Run(strconv.FormatInt(request, 10), func(t *testing.T) {
			e := awaitExit(t, startHelper(t, helperSpec("selflimits", strconv.FormatInt(request, 10))))
			if e.Code != 0 {
				t.Fatalf("self limit: %+v", e)
			}
		})
	}
}

func TestClampToHardLimit(t *testing.T) {
	for _, tc := range []struct {
		request    int64
		hard, want rlimValue
	}{{512 << 20, 1 << 30, 512 << 20}, {2 << 30, 1 << 30, 1 << 30}, {1 << 30, 1 << 30, 1 << 30}, {0, 1 << 30, 0}, {1 << 30, 0, 0}, {1 << 62, 1<<63 - 1, 1 << 62}, {-1, 1 << 30, 0}} {
		if got := clampToHardLimit(tc.request, tc.hard); got != tc.want {
			t.Errorf("clamp(%d,%d)=%d want %d", tc.request, tc.hard, got, tc.want)
		}
	}
}
