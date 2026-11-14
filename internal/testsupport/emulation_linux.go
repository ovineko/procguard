//go:build linux

package testsupport

import (
	"debug/elf"
	"fmt"
	"os"
)

func userEmulation() (bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate executable: %w", err)
	}
	binary, err := elf.Open(executable)
	if err != nil {
		return false, fmt.Errorf("read executable ELF: %w", err)
	}
	defer func() { _ = binary.Close() }()
	cpuinfo, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false, fmt.Errorf("read host CPU identity: %w", err)
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return false, fmt.Errorf("read emulator mappings: %w", err)
	}
	return emulationEvidence(binary.Machine, string(cpuinfo), string(maps)), nil
}
