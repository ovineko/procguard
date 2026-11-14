package procguard

import (
	"context"
	"fmt"
	"time"
)

// ParentExitError is the context.Cause of a WatchParent context cancelled because
// the watched parent is gone.
type ParentExitError struct {
	ExpectedPID int // The parentPID passed to WatchParent.
	ObservedPID int // getppid at detection time, typically 1 or a subreaper.
}

func (e *ParentExitError) Error() string {
	return fmt.Sprintf("parent process died (expected %d, now %d)", e.ExpectedPID, e.ObservedPID)
}

// WatchParent must be called by a cooperating child with its expected parent PID.
// Zero interval means five seconds; valid intervals are 500ms through 60s.
// Unix checks the parent PID immediately and polls for changes. A mismatch cancels
// the context with a *ParentExitError cause; use errors.As(context.Cause(ctx), &pe)
// with var pe *ParentExitError to inspect it. Parent context cancellation preserves
// its cause; calling cancel uses context.Canceled. The first cancellation wins.
// Linux also requests kernel SIGTERM delivery on parent death. procguard installs
// no signal handlers; the consumer controls how SIGTERM is handled.
// Cancel releases monitoring resources. Windows returns ErrUnsupported.
func WatchParent(ctx context.Context, parentPID int, pollInterval time.Duration) (context.Context, context.CancelFunc, error) {
	if parentPID <= 0 {
		return nil, nil, fmt.Errorf("parent PID must be positive: %d", parentPID)
	}
	if pollInterval == 0 {
		pollInterval = defaultPollInterval
	}
	if pollInterval < minPollInterval || pollInterval > maxPollInterval {
		return nil, nil, fmt.Errorf("parent poll interval must be between %v and %v", minPollInterval, maxPollInterval)
	}
	return watchParent(ctx, parentPID, pollInterval)
}
