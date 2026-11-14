package e2eprotocol

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeafRemoved(t *testing.T) {
	t.Run("real absent confirmed", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "leaf")
		if err := LeafRemoved(absent); err != nil {
			t.Fatalf("confirmed removal must be accepted, got %v", err)
		}
	})

	t.Run("real present rejected", func(t *testing.T) {
		present := filepath.Join(t.TempDir(), "leaf")
		if err := os.Mkdir(present, 0o700); err != nil {
			t.Fatal(err)
		}
		err := LeafRemoved(present)
		if err == nil || !strings.Contains(err.Error(), "still exists") {
			t.Fatalf("leftover leaf must be rejected, got %v", err)
		}
	})

	t.Run("present file rejected", func(t *testing.T) {
		present := filepath.Join(t.TempDir(), "leaf")
		if err := os.WriteFile(present, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := LeafRemoved(present); err == nil {
			t.Fatal("leftover leaf file must be rejected")
		}
	})

	t.Run("other stat error fails and wraps cause", func(t *testing.T) {
		cause := fmt.Errorf("injected: %w", fs.ErrPermission)
		original := leafStat
		leafStat = func(string) (fs.FileInfo, error) { return nil, cause }
		defer func() { leafStat = original }()

		err := LeafRemoved(filepath.Join(t.TempDir(), "leaf"))
		if err == nil {
			t.Fatal("inconclusive stat observation must fail the check")
		}
		if !errors.Is(err, fs.ErrPermission) || !errors.Is(err, cause) {
			t.Fatalf("failure must wrap the stat cause, got %v", err)
		}
		if !strings.Contains(err.Error(), "unverifiable") {
			t.Fatalf("failure must report the inconclusive observation, got %v", err)
		}
	})
}
