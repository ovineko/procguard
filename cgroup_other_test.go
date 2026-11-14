//go:build !linux

package procguard

// Non-Linux test builds never dispatch the cgroup helper modes: the tests
// that use them are guarded by the same build tag.
func helperCgroupSelfMax() int       { return 98 }
func helperCgroupAlloc([]string) int { return 98 }
func helperCgroupSpawn([]string) int { return 98 }
