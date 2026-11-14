package testsupport

import (
	"debug/elf"
	"os"
	"strings"
	"testing"
)

// SkipAddressSpace excludes emulators whose own mappings invalidate RLIMIT_AS.
func SkipAddressSpace(t *testing.T) {
	t.Helper()
	// The runner that chose a foreign --platform knows it is emulating; qemu-user
	// presents a guest-looking /proc/cpuinfo, so detection is only the fallback.
	emulated, declared := declaredEmulation()
	var err error
	if !declared {
		emulated, err = userEmulation()
	}
	if err != nil {
		t.Fatalf("detect user emulation: %v", err)
	}
	if emulated {
		t.Skip("qemu-user emulation: RLIMIT_AS constrains the emulator's reserved address space, not target memory")
	}
}

// declaredEmulation reads PROCGUARD_TEST_EMULATED ("1" or "0"); the second result
// reports whether the runner declared it at all.
func declaredEmulation() (bool, bool) {
	switch os.Getenv("PROCGUARD_TEST_EMULATED") {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

func cpuFamily(info string) string {
	switch {
	case strings.Contains(info, "vendor_id") && (strings.Contains(info, "GenuineIntel") || strings.Contains(info, "AuthenticAMD")), strings.Contains(info, "flags\t"):
		return "x86"
	case strings.Contains(info, "CPU implementer"), strings.Contains(info, "CPU architecture"):
		return "arm"
	case strings.Contains(info, "isa") && strings.Contains(info, "rv"):
		return "riscv"
	case strings.Contains(info, "POWER"), strings.Contains(info, "PowerPC"):
		return "ppc"
	case strings.Contains(info, "IBM/S390"):
		return "s390"
	}
	return ""
}

var machineFamilies = map[elf.Machine]string{
	elf.EM_386:     "x86",
	elf.EM_X86_64:  "x86",
	elf.EM_ARM:     "arm",
	elf.EM_AARCH64: "arm",
	elf.EM_RISCV:   "riscv",
	elf.EM_PPC:     "ppc",
	elf.EM_PPC64:   "ppc",
	elf.EM_S390:    "s390",
}

func machineFamily(machine elf.Machine) string { return machineFamilies[machine] }

func emulationEvidence(machine elf.Machine, cpuinfo, maps string) bool {
	for line := range strings.SplitSeq(maps, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && strings.Contains(fields[1], "x") {
			path := fields[5]
			if strings.Contains(path, "/qemu-") {
				return true
			}
		}
	}
	host, guest := cpuFamily(cpuinfo), machineFamily(machine)
	// Native compatibility execution (386 on amd64, arm on arm64) shares a family.
	return host != "" && guest != "" && host != guest
}
