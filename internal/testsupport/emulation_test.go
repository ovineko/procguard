package testsupport

import (
	"debug/elf"
	"testing"
)

func TestEmulationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		machine   elf.Machine
		cpu, maps string
		want      bool
	}{
		{"native-amd64", elf.EM_X86_64, "vendor_id : GenuineIntel", "", false},
		{"compat-386", elf.EM_386, "vendor_id : AuthenticAMD", "", false},
		{"native-arm64", elf.EM_AARCH64, "CPU implementer : 0x41", "", false},
		{"compat-arm", elf.EM_ARM, "CPU architecture : 8", "", false},
		{"native-riscv", elf.EM_RISCV, "isa : rv64imafdc", "", false},
		{"qemu-arm64", elf.EM_AARCH64, "vendor_id : GenuineIntel", "", true},
		{"qemu-arm", elf.EM_ARM, "vendor_id : GenuineIntel", "", true},
		{"qemu-riscv", elf.EM_RISCV, "CPU implementer : 0x41", "", true},
		{"same-arch-qemu", elf.EM_X86_64, "vendor_id : GenuineIntel", "100-200 r-xp 0 00:00 1 /usr/bin/qemu-x86_64-static", true},
		{"data-is-not-emulator", elf.EM_X86_64, "vendor_id : GenuineIntel", "100-200 r--p 0 00:00 1 /tmp/qemu-data", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := emulationEvidence(tc.machine, tc.cpu, tc.maps); got != tc.want {
				t.Fatalf("emulated=%t want %t", got, tc.want)
			}
		})
	}
}

func TestScale(t *testing.T) {
	t.Setenv(timeScaleKey, "8")
	if Scale(123) != 984 {
		t.Fatal("scale not applied")
	}
	for _, value := range []string{"0", "-1", "bad", "1.5", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(timeScaleKey, value)
			defer func() {
				if recover() == nil {
					t.Fatal("invalid scale accepted")
				}
			}()
			Scale(2)
		})
	}
}

func TestDeclaredEmulation(t *testing.T) {
	for _, tc := range []struct {
		value    string
		emulated bool
		declared bool
	}{
		{"", false, false}, {"0", false, true}, {"1", true, true}, {"unknown", false, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("PROCGUARD_TEST_EMULATED", tc.value)
			emulated, declared := declaredEmulation()
			if emulated != tc.emulated || declared != tc.declared {
				t.Fatalf("emulated=%v declared=%v", emulated, declared)
			}
		})
	}
}

func TestUnknownAndAdditionalCPUFamilies(t *testing.T) {
	for _, tc := range []struct {
		machine elf.Machine
		cpu     string
		want    bool
	}{
		{elf.EM_PPC64, "POWER", false},
		{elf.EM_S390, "IBM/S390", false},
		{elf.EM_X86_64, "POWER", true},
		{elf.EM_X86_64, "IBM/S390", true},
		{elf.EM_X86_64, "unknown", false},
		{elf.EM_NONE, "vendor_id : GenuineIntel", false},
	} {
		if got := emulationEvidence(tc.machine, tc.cpu, ""); got != tc.want {
			t.Errorf("machine=%v cpu=%q emulated=%v", tc.machine, tc.cpu, got)
		}
	}
}
