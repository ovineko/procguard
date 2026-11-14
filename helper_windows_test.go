//go:build windows

package procguard

import (
	"os"
	"time"
)

func helperSignals(_ string) <-chan os.Signal {
	signals := make(chan os.Signal)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
		}
	}()
	return signals
}
func helperSelfLimits(_ []string) { os.Exit(98) }
