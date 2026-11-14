//go:build linux

package procguard

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ovineko/procguard/internal/testsupport"
)

func TestParentLimits(t *testing.T) {
	score := 100
	for _, tc := range []struct {
		name       string
		limits     Limits
		file, want string
	}{
		{"oom", Limits{OOMScoreAdj: &score}, "oom_score_adj", "100"},
		{"address-space", Limits{AddressSpace: 1 << 40}, "limits", "1099511627776"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "address-space" && raceEnabled {
				t.Skip("RLIMIT_AS conflicts with ThreadSanitizer memory needs")
			}
			if tc.name == "address-space" {
				testsupport.SkipAddressSpace(t)
			}
			spec := helperSpec("wait")
			spec.Limits = tc.limits
			c := startHelper(t, spec)
			data, err := os.ReadFile("/proc/" + strconv.Itoa(c.PID()) + "/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Fatalf("limit %s missing in %s", tc.want, data)
			}
		})
	}
}

func TestLimitFailureCleansChild(t *testing.T) {
	score := 1001
	spec := helperSpec("wait")
	spec.Limits.OOMScoreAdj = &score
	calls, pid := 0, 0
	spec.AfterStart = func(childPID int, err error) {
		calls++
		pid = childPID
		if err == nil {
			t.Error("missing limit error")
		}
	}
	c, err := Start(t.Context(), spec)
	if c != nil || err == nil || calls != 1 || pid <= 0 {
		t.Fatalf("failure: %v %v %d %d", c, err, calls, pid)
	}
	if !processGone(t, pid) {
		t.Fatal("failed launch left child alive")
	}
}

func helperSelfOOM(args []string) {
	score, err := strconv.Atoi(args[0])
	if err != nil {
		os.Exit(98)
	}
	if ApplyLimitsToSelf(Limits{OOMScoreAdj: &score}) != nil {
		os.Exit(98)
	}
	data, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil || strings.TrimSpace(string(data)) != args[0] {
		os.Exit(98)
	}
	os.Exit(0)
}

func TestSelfOOM(t *testing.T) {
	for _, value := range []string{"0", "100", "1000"} {
		t.Run(value, func(t *testing.T) {
			if e := awaitExit(t, startHelper(t, helperSpec("selfoom", value))); e.Code != 0 {
				t.Fatalf("self OOM: %+v", e)
			}
		})
	}
}
