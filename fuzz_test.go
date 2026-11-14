package procguard

import (
	"context"
	"testing"
	"time"
)

func FuzzLimitsValidate(f *testing.F) {
	f.Fuzz(func(t *testing.T, address int64, score int, present bool) {
		limits := Limits{AddressSpace: address}
		if present {
			limits.OOMScoreAdj = &score
		}
		if err := limits.validate(); (err != nil) != (address < 0) {
			t.Fatalf("validation: %+v: %v", limits, err)
		}
	})
}

func FuzzSpecValidate(f *testing.F) {
	f.Fuzz(func(t *testing.T, path string, grace, address int64, cancelled bool) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelled {
			cancel()
		}
		err := validateSpec(ctx, Spec{Path: path, GracePeriod: time.Duration(grace), Limits: Limits{AddressSpace: address}})
		if (path == "" || grace < 0 || address < 0 || cancelled) && err == nil {
			t.Fatal("invalid spec accepted")
		}
	})
}

func FuzzOptionsValidate(f *testing.F) {
	f.Fuzz(func(t *testing.T, mode, buffer int, factory bool) {
		// Valid buffers allocate channels; bound them while retaining negative inputs.
		buffer %= 1024
		opts := Options{Restart: RestartPolicy{Mode: RestartMode(mode)}, EventBuffer: buffer}
		if factory {
			opts.Spec = func(context.Context) (Spec, error) { return Spec{}, nil }
		}
		_, err := NewSupervisor(opts)
		invalid := !factory || buffer < 0 || mode < int(RestartAlways) || mode > int(RestartNever)
		if (err != nil) != invalid {
			t.Fatalf("validation: mode=%d buffer=%d factory=%t: %v", mode, buffer, factory, err)
		}
	})
}

func FuzzBackoff(f *testing.F) {
	f.Fuzz(func(t *testing.T, maximum, stableAfter int64, calls []byte) {
		b := NewBackoff(time.Duration(maximum), time.Duration(stableAfter))
		limit := time.Duration(maximum)
		if limit <= 0 {
			limit = defaultMaxDelay
		}
		threshold := time.Duration(stableAfter)
		if threshold <= 0 {
			threshold = defaultStableAfter
		}
		var want time.Duration
		failures := 0
		for _, call := range calls {
			switch call % 3 {
			case 0:
				b.Reset()
				want = 0
				failures = 0
			case 1:
				previous := want
				switch {
				case failures == 0:
					want = min(time.Second, limit)
				case want >= limit-want:
					want = limit
				default:
					want *= 2
				}
				failures++
				if got := b.Next(); got != want || got < previous || got > limit || got < 0 {
					t.Fatalf("Next=%v want=%v previous=%v max=%v", got, want, previous, limit)
				}
			}
			for range 2 {
				if b.Current() != want || b.Failures() != failures || b.Saturated() != (failures > 0 && want == limit) {
					t.Fatal("observation changed backoff")
				}
			}
			if !b.Stable(threshold) || b.Stable(threshold-1) {
				t.Fatal("stable boundary")
			}
		}
	})
}

func FuzzWatchParentValidate(f *testing.F) {
	f.Fuzz(func(t *testing.T, pid int, interval int64) {
		ctx, cancel, err := WatchParent(t.Context(), pid, time.Duration(interval))
		if cancel != nil {
			cancel()
		}
		if (pid <= 0 || (interval != 0 && (interval < int64(minPollInterval) || interval > int64(maxPollInterval)))) && (err == nil || ctx != nil) {
			t.Fatal("invalid watch accepted")
		}
	})
}
