//go:build openbsd

package procguard

import "os"

func helperSelfLimits(_ []string) { os.Exit(98) }
