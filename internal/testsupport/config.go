// Package testsupport owns test environment inputs and bounded timing.
package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var executable = func() string {
	path, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("locate test executable: %v", err))
	}
	path, err = filepath.Abs(path)
	if err != nil {
		panic(fmt.Sprintf("resolve test executable path: %v", err))
	}
	return path
}()

const (
	timeScaleKey = "PROCGUARD_TEST_TIME_SCALE"
	modelRunsKey = "PROCGUARD_MODEL_RUNS"
	// HelperKey marks test-binary subprocesses.
	HelperKey = "PROCGUARD_TEST_HELPER"
	// ValueKey carries the environment-inheritance test payload.
	ValueKey = "PROCGUARD_TEST_VALUE"
)

// Scale multiplies wall-clock budgets, rejecting invalid settings and overflow.
func Scale(d time.Duration) time.Duration {
	scale := positiveInt(timeScaleKey, 1)
	if d < 0 || (d > 0 && int64(scale) > int64(1<<63-1)/int64(d)) {
		panic("test duration overflow")
	}
	return d * time.Duration(scale)
}

// ModelRuns defaults to 200 reproducible schedules.
func ModelRuns() int { return positiveInt(modelRunsKey, 200) }

func positiveInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		panic(key + " must be a positive integer")
	}
	return n
}

// Helper distinguishes re-executed helpers from the test runner.
func Helper() bool { return os.Getenv(HelperKey) == "1" }

// Value is the environment-inheritance test payload.
func Value() string { return os.Getenv(ValueKey) }

// Executable captures an absolute path before tests can change os.Args[0].
func Executable() string { return executable }
