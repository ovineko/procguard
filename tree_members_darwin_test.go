//go:build darwin

package procguard

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinGroupLivenessIncludesUnsignalableLeader(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pid, pgid     int32
		state         int8
		excludeLeader bool
		want          bool
	}{
		{"live leader blocks permission-error recovery", 42, 42, 2, false, true},
		{"live leader is excluded from descendant cleanup", 42, 42, 2, true, false},
		{"zombie leader is not live", 42, 42, darwinZombie, false, false},
		{"live descendant blocks recovery", 43, 42, 2, false, true},
		{"live descendant needs cleanup", 43, 42, 2, true, true},
		{"unknown state remains conservative", 43, 42, 0, false, true},
		{"unrelated group is ignored", 44, 44, 2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var process unix.KinfoProc
			process.Proc.P_pid = tc.pid
			process.Eproc.Pgid = tc.pgid
			process.Proc.P_stat = tc.state
			if got := darwinGroupHasLiveProcesses([]unix.KinfoProc{process}, 42, tc.excludeLeader); got != tc.want {
				t.Fatalf("group live=%t want=%t", got, tc.want)
			}
		})
	}
}
