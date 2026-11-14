package procguard

// CgroupConfig requests Linux cgroup v2 isolation for one launch. A non-nil
// Cgroup in Spec is an explicit, fail-closed request: when the requested
// isolation or limit cannot be provided, Start fails before any child code
// runs instead of launching an uncapped or uncontained tree.
//
// The launch keeps its process group alongside the cgroup, so group SIGTERM
// stays the graceful stop and the cgroup adds atomic membership: hard kills
// and liveness come from the leaf, immune to setsid escapes. Requests are
// never silently downgraded to a plain process group.
type CgroupConfig struct {
	// Parent is the delegated cgroup v2 directory the launch creates its
	// private leaf under. Empty derives the parent from the launching
	// process's own cgroup; that derivation usually cannot enable controllers
	// while the launcher itself remains a member of the parent, so an explicit
	// delegated parent is the reliable form wherever limits are requested.
	// A relative path is resolved against the cgroup v2 mount.
	Parent string

	// MemoryMax caps the whole tree in bytes through the leaf's memory.max;
	// the kernel applies it at clone, before any child code runs. The leaf's
	// swap is pinned to zero so the cap is enforced by the kernel's memcg
	// OOM kill rather than silently absorbed by reclaim. Zero isolates the
	// tree without a memory limit. Negative values are invalid. This is
	// cgroup memory accounting, not RLIMIT_AS and not RSS.
	MemoryMax int64
}
