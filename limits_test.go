package procguard

import "testing"

func TestLimitsValidation(t *testing.T) {
	for _, value := range []int64{-1, -1 << 40} {
		if err := ApplyLimitsToSelf(Limits{AddressSpace: value}); err == nil {
			t.Errorf("negative limit %d accepted", value)
		}
	}
	if err := ApplyLimitsToSelf(Limits{}); err != nil {
		t.Fatal(err)
	}
}
