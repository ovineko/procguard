//go:build !linux

package procguard

import (
	"fmt"
	"os/exec"
)

// cgroupResources exists so shared launch code compiles on every platform;
// non-Linux platforms reject the request during validation and never reach
// resource creation.
type cgroupResources struct{}

func validateCgroupSpec(cfg *CgroupConfig) error {
	return fmt.Errorf("cgroup isolation: %w", ErrUnsupported)
}

func newCgroupResources(cfg *CgroupConfig) (*cgroupResources, error) {
	return nil, fmt.Errorf("cgroup isolation: %w", ErrUnsupported)
}

func (*cgroupResources) attach(*exec.Cmd)         {}
func (*cgroupResources) remaining() (bool, error) { return false, nil }
func (*cgroupResources) kill() error              { return nil }
func (*cgroupResources) close() error             { return nil }

func (*cgroupResources) discard() error { return nil }
