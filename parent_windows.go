//go:build windows

package procguard

import (
	"context"
	"fmt"
	"time"
)

func watchParent(_ context.Context, _ int, _ time.Duration) (context.Context, context.CancelFunc, error) {
	return nil, nil, fmt.Errorf("parent watching: %w", ErrUnsupported)
}
