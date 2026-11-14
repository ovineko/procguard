//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ovineko/procguard/internal/testsupport"

	wire "github.com/ovineko/procguard/internal/e2eprotocol"
)

func TestNative(t *testing.T) {
	root := wire.Root()
	if root == "" {
		root = ".."
	}
	CheckCoverage(t, root)
	fixture := wire.Fixture()
	if fixture == "" {
		fixture = BuildFixture(t, root, runtime.GOOS, runtime.GOARCH)
	} else {
		var err error
		fixture, err = filepath.Abs(fixture)
		if err != nil {
			t.Fatal(err)
		}
	}
	class, err := Platform(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Scenarios {
		t.Run(s.ID, func(t *testing.T) {
			if reason := s.Expectations[class].Reason; reason != "" {
				if s.ID != "cgroup-v2" || os.Getenv("PROCGUARD_E2E_CGROUP_REQUIRE") != "1" {
					t.Skip(reason)
				}
			}
			if s.ID == "parent-limits" || s.ID == "self-address-space" {
				testsupport.SkipAddressSpace(t)
			}
			mode := s.Mode
			if s.ID == "watch-parent" && class == Windows {
				mode = "supervise"
			}
			records := RunNative(t, fixture, mode, "-scenario", s.ID, "-dir", t.TempDir())
			Assert(t, s, class, runtime.GOOS, records)
		})
	}
}
