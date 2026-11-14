package e2eprotocol

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// leafStat is the narrow seam fixture tests use to inject filesystem
// failures; the production assertion observes real os.Stat results only.
var leafStat = os.Stat

// LeafRemoved verifies that a cgroup launch leaf directory no longer exists
// after the unit finished. A successful Stat means the leaf remains and is
// rejected; fs.ErrNotExist is the only observation that confirms removal;
// any other stat error is inconclusive and fails the check while wrapping
// its cause, so a permission or I/O problem can never pass for absence.
// The fixture and the harness assert the same rule through this helper.
func LeafRemoved(path string) error {
	_, err := leafStat(path)
	switch {
	case err == nil:
		return fmt.Errorf("launch leaf %s still exists after the unit finished", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("launch leaf %s removal unverifiable: %w", path, err)
	}
}
