package procguard

import (
	"math/rand/v2"
	"sync"
	"time"
)

const (
	defaultMaxDelay       = 60 * time.Second
	defaultStableAfter    = 5 * time.Minute
	defaultGracePeriod    = 10 * time.Second
	defaultEventBuffer    = 64
	waitDrainAllowance    = 2 * time.Second
	treePollInterval      = 25 * time.Millisecond
	escalationDrainWindow = 2 * time.Second
	defaultPollInterval   = 5 * time.Second
	minPollInterval       = 500 * time.Millisecond
	maxPollInterval       = 60 * time.Second
)

// Backoff is safe for concurrent use. Its zero value uses the defaults; it must
// not be copied after first use. Configuration is immutable after construction.
type Backoff struct {
	mu                     sync.Mutex
	max, stableAfter, base time.Duration
	failures               int
	rng                    *rand.Rand
	lastDraw               time.Duration
}

// BackoffOptions configures a Backoff; zero fields take the same defaults as
// NewBackoff.
type BackoffOptions struct {
	// Base is the delay after the first failure; it doubles from here.
	// Non-positive means one second. A Base above Maximum is capped.
	Base time.Duration
	// Maximum caps every delay. Non-positive means sixty seconds.
	Maximum time.Duration
	// StableAfter is the runtime that counts a launch as stable.
	// Non-positive means five minutes.
	StableAfter time.Duration
	// Rand enables bounded jitter: every scheduled attempt draws its delay
	// uniformly from [Base, deterministic delay]. The first attempt is fixed
	// at Base (the deterministic delay equals Base there), and Base ==
	// Maximum removes randomness entirely. The provided generator is used
	// exclusively by this Backoff — its accesses are serialized by this
	// Backoff's mutex, but sharing one *rand.Rand between several Backoffs
	// is not synchronized by them. Nil keeps the deterministic sequence.
	// Draw once per attempt: Current reports the drawn value instead of
	// resampling, and Saturated stays deterministic.
	Rand *rand.Rand
}

// NewBackoff uses 60 seconds and five minutes for non-positive arguments.
func NewBackoff(maximum, stableAfter time.Duration) *Backoff {
	return NewBackoffWithOptions(BackoffOptions{Maximum: maximum, StableAfter: stableAfter})
}

// NewBackoffWithOptions builds a Backoff with an explicit base delay and an
// optional randomness source for jitter.
func NewBackoffWithOptions(opts BackoffOptions) *Backoff {
	base := opts.Base
	if base <= 0 {
		base = time.Second
	}
	maximum := opts.Maximum
	if maximum <= 0 {
		maximum = defaultMaxDelay
	}
	stableAfter := opts.StableAfter
	if stableAfter <= 0 {
		stableAfter = defaultStableAfter
	}
	return &Backoff{max: maximum, stableAfter: stableAfter, base: base, rng: opts.Rand}
}

// Next records a failure and returns the delay for that attempt: Base,
// 2·Base, 4·Base, ... capped at the maximum, or a full-jitter draw from
// [Base, the deterministic delay] when a randomness source is configured.
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < int(^uint(0)>>1) {
		b.failures++
	}
	b.lastDraw = b.draw()
	return b.lastDraw
}

// Current is zero until the first failure and does not advance the streak.
// With jitter it reports the delay the last attempt actually drew.
func (b *Backoff) Current() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures == 0 {
		return 0
	}
	return b.lastDraw
}

// Reset makes the next failure start at the shortest delay.
func (b *Backoff) Reset() { b.mu.Lock(); defer b.mu.Unlock(); b.failures = 0 }

// Failures counts failures since the last reset, including those at the cap.
func (b *Backoff) Failures() int { b.mu.Lock(); defer b.mu.Unlock(); return b.failures }

// Saturated is false before the first failure, even for a very small maximum.
func (b *Backoff) Saturated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures > 0 && b.delay() >= b.maximum()
}

// Stable includes runtimes exactly equal to the configured threshold.
func (b *Backoff) Stable(runtime time.Duration) bool {
	threshold := b.stableAfter
	if threshold <= 0 {
		threshold = defaultStableAfter
	}
	return runtime >= threshold
}

// draw returns the deterministic delay, resampled into [Base, deterministic]
// when jitter is configured. Saturated and the deterministic sequence stay
// independent of the drawn value.
func (b *Backoff) draw() time.Duration {
	deterministic := b.delay()
	if b.rng == nil || deterministic <= b.base {
		return deterministic
	}
	return b.base + time.Duration(b.rng.Int64N(int64(deterministic-b.base)+1))
}

func (b *Backoff) maximum() time.Duration {
	if b.max <= 0 {
		return defaultMaxDelay
	}
	return b.max
}

func (b *Backoff) delay() time.Duration {
	if b.failures == 0 {
		return 0
	}
	delay := b.base
	if delay <= 0 {
		delay = time.Second
	}
	maximum := b.maximum()
	for n := 1; n < b.failures && delay < maximum; n++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return min(delay, maximum)
}
