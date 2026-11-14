package procguard

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

func TestNewBackoff(t *testing.T) {
	t.Run("custom values", func(t *testing.T) {
		bc := NewBackoff(30*time.Second, 10*time.Minute)
		if bc.max != 30*time.Second {
			t.Errorf("max = %v, want 30s", bc.max)
		}
		if bc.stableAfter != 10*time.Minute {
			t.Errorf("stableAfter = %v, want 10m", bc.stableAfter)
		}
		if bc.failures != 0 {
			t.Errorf("failures = %d, want 0", bc.failures)
		}
	})

	t.Run("defaults for zero values", func(t *testing.T) {
		bc := NewBackoff(0, 0)
		if bc.max != defaultMaxDelay {
			t.Errorf("max = %v, want %v", bc.max, defaultMaxDelay)
		}
		if bc.stableAfter != defaultStableAfter {
			t.Errorf("stableAfter = %v, want %v", bc.stableAfter, defaultStableAfter)
		}
	})

	t.Run("defaults for negative values", func(t *testing.T) {
		bc := NewBackoff(-1, -1)
		if bc.max != defaultMaxDelay {
			t.Errorf("max = %v, want %v", bc.max, defaultMaxDelay)
		}
		if bc.stableAfter != defaultStableAfter {
			t.Errorf("stableAfter = %v, want %v", bc.stableAfter, defaultStableAfter)
		}
	})
}

func TestBackoff_Next(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{"first failure", 1, 1 * time.Second},
		{"second failure", 2, 2 * time.Second},
		{"third failure", 3, 4 * time.Second},
		{"fourth failure", 4, 8 * time.Second},
		{"fifth failure", 5, 16 * time.Second},
		{"sixth failure", 6, 32 * time.Second},
		{"seventh failure capped", 7, 60 * time.Second},
		{"eighth failure capped", 8, 60 * time.Second},
		{"tenth failure capped", 10, 60 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := NewBackoff(defaultMaxDelay, defaultStableAfter)
			var got time.Duration
			for range tt.failures {
				got = bc.Next()
			}
			if got != tt.want {
				t.Errorf("Next() after %d failures = %v, want %v", tt.failures, got, tt.want)
			}
		})
	}
}

func TestBackoff_Next_Custommax(t *testing.T) {
	bc := NewBackoff(10*time.Second, defaultStableAfter)

	delays := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}

	for i, want := range delays {
		got := bc.Next()
		if got != want {
			t.Errorf("call %d: Next() = %v, want %v", i+1, got, want)
		}
	}
}

func TestBackoff_Reset(t *testing.T) {
	bc := NewBackoff(defaultMaxDelay, defaultStableAfter)

	bc.Next()
	bc.Next()
	bc.Next()

	if bc.Failures() != 3 {
		t.Errorf("Failures() = %d, want 3", bc.Failures())
	}

	bc.Reset()

	if bc.Failures() != 0 {
		t.Errorf("Failures() after Reset() = %d, want 0", bc.Failures())
	}

	got := bc.Next()
	if got != 1*time.Second {
		t.Errorf("Next() after Reset() = %v, want 1s", got)
	}
}

func TestBackoff_Stable(t *testing.T) {
	tests := []struct {
		name            string
		uptime          time.Duration
		stableThreshold time.Duration
		want            bool
	}{
		{"below threshold", 4 * time.Minute, 5 * time.Minute, false},
		{"at threshold", 5 * time.Minute, 5 * time.Minute, true},
		{"above threshold", 10 * time.Minute, 5 * time.Minute, true},
		{"zero uptime", 0, 5 * time.Minute, false},
		{"just under threshold", 4*time.Minute + 59*time.Second, 5 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := NewBackoff(defaultMaxDelay, tt.stableThreshold)
			if got := bc.Stable(tt.uptime); got != tt.want {
				t.Errorf("Stable(%v) = %v, want %v", tt.uptime, got, tt.want)
			}
		})
	}
}

func TestBackoff_Saturated(t *testing.T) {
	tests := []struct {
		name     string
		maxDelay time.Duration
		failures int
		want     bool
	}{
		{"zero failures", 60 * time.Second, 0, false},
		{"first failure", 60 * time.Second, 1, false},
		{"second failure", 60 * time.Second, 2, false},
		{"sixth failure 32s", 60 * time.Second, 6, false},
		{"seventh failure at max", 60 * time.Second, 7, true},
		{"eighth failure still at max", 60 * time.Second, 8, true},
		{"small max first at max", 4 * time.Second, 3, true},
		{"small max not yet", 4 * time.Second, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := NewBackoff(tt.maxDelay, defaultStableAfter)
			for range tt.failures {
				bc.Next()
			}
			if got := bc.Saturated(); got != tt.want {
				t.Errorf("Saturated() after %d failures = %v, want %v", tt.failures, got, tt.want)
			}
		})
	}
}

func TestBackoffFailureCounterSaturates(t *testing.T) {
	b := NewBackoff(time.Duration(1<<63-1), 0)
	b.failures = int(^uint(0)>>1) - 1
	for range 2 {
		if b.Next() != time.Duration(1<<63-1) || b.Failures() != int(^uint(0)>>1) {
			t.Fatal("backoff overflowed")
		}
	}
}

func TestNewBackoffWithOptionsBase(t *testing.T) {
	b := NewBackoffWithOptions(BackoffOptions{Base: 5 * time.Second, Maximum: 12 * time.Second})
	want := []time.Duration{5 * time.Second, 10 * time.Second, 12 * time.Second, 12 * time.Second}
	for i, d := range want {
		if got := b.Next(); got != d {
			t.Fatalf("attempt %d delay = %v, want %v", i+1, got, d)
		}
	}
	if got := b.Current(); got != 12*time.Second {
		t.Fatalf("Current = %v, want the cap", got)
	}

	zero := NewBackoffWithOptions(BackoffOptions{})
	if got := zero.Next(); got != time.Second {
		t.Fatalf("zero options delay = %v, want 1s", got)
	}

	over := NewBackoffWithOptions(BackoffOptions{Base: 30 * time.Second, Maximum: 10 * time.Second})
	if got := over.Next(); got != 10*time.Second {
		t.Fatalf("base above maximum delay = %v, want the maximum", got)
	}

	// Doubling must not overflow: a huge base saturates at the maximum.
	huge := NewBackoffWithOptions(BackoffOptions{Base: 1 << 62, Maximum: 1 << 62})
	for range 4 {
		if got := huge.Next(); got != 1<<62 {
			t.Fatalf("huge base delay = %v, want the maximum", got)
		}
	}
}

func TestBackoffJitterBoundsAndSingleDraw(t *testing.T) {
	const base, maximum = 2 * time.Second, 8 * time.Second
	b := NewBackoffWithOptions(BackoffOptions{Base: base, Maximum: maximum, Rand: rand.New(rand.NewPCG(42, 42))})
	deterministic := []time.Duration{base, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for attempt, det := range deterministic {
		drawn := b.Next()
		if drawn < base || drawn > det {
			t.Fatalf("attempt %d draw = %v, want within [%v, %v]", attempt+1, drawn, base, det)
		}
		if again := b.Current(); again != drawn {
			t.Fatalf("Current = %v, want the single draw %v", again, drawn)
		}
	}

	// Saturated is a property of the deterministic ceiling, not the draw.
	if !b.Saturated() {
		t.Fatal("backoff at the cap must be saturated regardless of jitter")
	}
	b.Reset()
	if got := b.Current(); got != 0 {
		t.Fatalf("Current after reset = %v, want zero", got)
	}
}

// Degenerate bounds stay exact under jitter: the first attempt is fixed at
// Base, and Base == Maximum removes randomness entirely.
func TestBackoffJitterDegenerateBounds(t *testing.T) {
	b := NewBackoffWithOptions(BackoffOptions{Base: 3 * time.Second, Maximum: 3 * time.Second, Rand: rand.New(rand.NewPCG(11, 11))})
	for attempt := range 5 {
		if got := b.Next(); got != 3*time.Second {
			t.Fatalf("attempt %d draw = %v, want exactly 3s", attempt+1, got)
		}
	}
	b.Reset()
	if got := b.Next(); got != 3*time.Second {
		t.Fatalf("first attempt after reset = %v, want the fixed base 3s", got)
	}
}

func TestBackoffConcurrentJitterStaysInBounds(t *testing.T) {
	const base, maximum = time.Second, 4 * time.Second
	b := NewBackoffWithOptions(BackoffOptions{Base: base, Maximum: maximum, Rand: rand.New(rand.NewPCG(7, 7))})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				d := b.Next()
				if d < base || d > maximum {
					panic(fmt.Sprintf("draw %v outside [%v, %v]", d, base, maximum))
				}
			}
		})
	}
	wg.Wait()
}
