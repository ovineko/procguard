//go:build linux

package procguard

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const cgroupRoot = "/sys/fs/cgroup"

// writeSwapMax pins the leaf's swap; swapped in tests to exercise the
// fail-closed path when the pin is refused.
var writeSwapMax = func(leafPath, value string) error {
	return os.WriteFile(filepath.Join(leafPath, "memory.swap.max"), []byte(value+"\n"), 0o600)
}

var cgroupLeafCounter atomic.Uint64

// cgroupResources owns one launch subtree: the directory descriptor provides
// clone-time placement and cgroup.events accounts for nested live processes.
type cgroupResources struct {
	path  string
	dir   *os.File
	dirFD int
	// killFile reports that cgroup.kill exists (kernel 5.14+), making the
	// hard kill atomic for the whole subtree.
	killFile bool
}

// validateCgroupSpec checks what can be known without creating anything, so
// an impossible request fails before the launch sequence begins.
func validateCgroupSpec(cfg *CgroupConfig) error {
	if cfg.MemoryMax < 0 {
		return fmt.Errorf("cgroup memory limit must not be negative: %d", cfg.MemoryMax)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(cgroupRoot, &stat); err != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("cgroup isolation: cgroup v2 unified hierarchy is not mounted at %s", cgroupRoot)
	}
	if !cgroupFDPlacementSupported() {
		return errors.New("cgroup isolation needs clone3 CLONE_INTO_CGROUP (kernel 5.7+)")
	}
	return nil
}

// cgroupFDPlacementSupported reports kernel >= 5.7, the floor for clone3
// CLONE_INTO_CGROUP. Post-start migration is not an equivalent substitute
// for a launch contract whose limits must hold before child code runs.
func cgroupFDPlacementSupported() bool {
	var uname unix.Utsname
	if err := unix.Uname(&uname); err != nil {
		return false
	}
	release := releaseString(&uname)
	major, minor, ok := parseKernelRelease(release)
	if !ok {
		return false
	}
	return major > 5 || (major == 5 && minor >= 7)
}

func releaseString(uname *unix.Utsname) string {
	var buf []byte
	for _, c := range uname.Release {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c)) //nolint:unconvert // Release is [65]int8; the sign must be dropped per byte
	}
	return string(buf)
}

func parseKernelRelease(release string) (major, minor int, ok bool) {
	dot := strings.IndexByte(release, '.')
	if dot <= 0 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(release[:dot])
	if err != nil {
		return 0, 0, false
	}
	rest := release[dot+1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			rest = rest[:i]
			break
		}
	}
	minor, err = strconv.Atoi(rest)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// newCgroupResources creates the leaf and its limit before the child exists.
// Every failure removes the leaf and returns a reason naming the delegated
// parent, so an unsatisfiable request never reaches child code.
func newCgroupResources(cfg *CgroupConfig) (*cgroupResources, error) {
	if err := validateCgroupSpec(cfg); err != nil {
		return nil, err
	}
	parent := cgroupParentDir(cfg.Parent)
	info, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("cgroup isolation: delegated parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cgroup isolation: delegated parent %s is not a directory", parent)
	}
	path := filepath.Join(parent, cgroupLeafName())
	if err := os.Mkdir(path, 0o750); err != nil {
		return nil, fmt.Errorf("cgroup isolation: create leaf under %s: %w", parent, err)
	}
	r := &cgroupResources{path: path}
	fail := func(cause error) (*cgroupResources, error) {
		return nil, errors.Join(cause, os.Remove(path))
	}
	// A directory that does not behave as a cgroup v2 member would fail much
	// later, at clone, with an unrelated errno; refuse it here instead.
	if _, err := os.Stat(filepath.Join(path, "cgroup.procs")); err != nil {
		return fail(fmt.Errorf("cgroup isolation: %s is not a cgroup v2 directory: %w", parent, fs.ErrNotExist))
	}
	if cfg.MemoryMax > 0 {
		if err := enableController(parent, "memory"); err != nil {
			return fail(err)
		}
		limit := strconv.FormatInt(cfg.MemoryMax, 10) + "\n"
		if err := os.WriteFile(filepath.Join(path, "memory.max"), []byte(limit), 0o600); err != nil {
			return fail(fmt.Errorf("cgroup isolation: set memory.max in %s: %w", path, err))
		}
		// Swap would let the tree exceed the cap indefinitely through
		// reclaim; the advertised contract is a hard cap, so a failed pin is
		// a failed launch. Only a host without swap control at all (no
		// memory.swap.max file, typically swapless) proceeds without the pin.
		if err := writeSwapMax(path, "0"); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return fail(fmt.Errorf("cgroup isolation: pin memory.swap.max in %s: %w", path, err))
			}
		}
	}
	// The leaf directory stays open for clone-time placement; its path is
	// this launch's own delegated leaf, not caller-supplied file input.
	//nolint:gosec // G304: the leaf path is procguard-created under the delegated parent
	dir, err := os.Open(path)
	if err != nil {
		return fail(fmt.Errorf("cgroup isolation: open leaf %s: %w", path, err))
	}
	r.dir = dir
	r.dirFD = int(dir.Fd())
	r.killFile = fileExists(filepath.Join(path, "cgroup.kill"))
	return r, nil
}

func cgroupParentDir(parent string) string {
	if parent == "" {
		rel, err := selfCgroupRelative()
		if err == nil {
			return filepath.Join(cgroupRoot, rel)
		}
		return cgroupRoot
	}
	if filepath.IsAbs(parent) {
		return parent
	}
	return filepath.Join(cgroupRoot, parent)
}

func cgroupLeafName() string {
	var salt [2]byte
	_, _ = rand.Read(salt[:])
	return fmt.Sprintf("pguard.%d.%d.%s", os.Getpid(), cgroupLeafCounter.Add(1), hex.EncodeToString(salt[:]))
}

// selfCgroupRelative parses the "0::/path" record of /proc/self/cgroup.
func selfCgroupRelative() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		after, ok := strings.CutPrefix(line, "0::")
		if ok && after != "" {
			return strings.TrimPrefix(after, "/"), nil
		}
	}
	return "", errors.New("no v2 record")
}

// enableController turns a controller on in the delegated parent's
// subtree_control. Under the no-internal-process rule a parent that still
// holds member processes cannot enable one, so the error names the
// controller state and points at the usual cause.
func enableController(parent, controller string) error {
	//nolint:gosec // the path is the delegated cgroup directory from CgroupConfig
	available, err := os.ReadFile(filepath.Join(parent, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("cgroup isolation: %s is not a cgroup v2 directory (%w)", parent, err)
	}
	err = os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+"+controller+"\n"), 0o600)
	if err == nil {
		return nil
	}
	return fmt.Errorf("cgroup isolation: enable %s controller in %s: %w (available controllers: %s; "+
		"a parent holding member processes cannot enable controllers — move the launcher's own leaf out of it)",
		controller, parent, err, strings.TrimSpace(string(available)))
}

func (r *cgroupResources) attach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// clone3 CLONE_INTO_CGROUP: the child is born inside the leaf and its
	// forks inherit membership, so limits hold before any child code runs.
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = r.dirFD
}

// Membership snapshots exclude exiting tasks, including unreaped zombies.
func (r *cgroupResources) members() ([]int, error) { return readMembers(r) }

var readMembers = func(r *cgroupResources) ([]int, error) {
	var pids []int
	err := filepath.WalkDir(r.path, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != "cgroup.procs" {
			return nil
		}
		//nolint:gosec // G304: WalkDir supplies cgroup.procs paths in the launch-owned subtree.
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read cgroup membership %s: %w", path, err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			pid, err := strconv.Atoi(line)
			if err != nil || pid < 1 {
				return fmt.Errorf("parse cgroup membership %s: invalid PID %q", path, line)
			}
			pids = append(pids, pid)
		}
		return nil
	})

	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("enumerate cgroup subtree: %w", err)
	}
	return pids, nil
}

func (r *cgroupResources) populated() (bool, error) {
	data, err := os.ReadFile(filepath.Join(r.path, "cgroup.events"))
	if err != nil {
		return false, fmt.Errorf("read cgroup.events: %w", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "populated" {
			continue
		}
		if len(fields) != 2 || (fields[1] != "0" && fields[1] != "1") {
			return false, fmt.Errorf("invalid cgroup populated state: %q", line)
		}
		return fields[1] == "1", nil
	}
	return false, errors.New("cgroup.events lacks populated state")
}

func (r *cgroupResources) remaining() (bool, error) {
	populated, err := r.populated()
	if !errors.Is(err, fs.ErrNotExist) {
		return populated, err
	}
	pids, err := r.members()
	return len(pids) != 0, err
}

// writeCgroupKill delivers the atomic subtree kill; swapped in tests to
// exercise a refused write on an existing cgroup.kill.
var writeCgroupKill = func(leafPath string) error {
	return os.WriteFile(filepath.Join(leafPath, "cgroup.kill"), []byte("1\n"), 0o600)
}

// cgroup.kill is atomic for the whole subtree. The per-PID fallback pins every
// nested member and re-checks subtree membership before signalling, so PID reuse
// cannot redirect the signal. The sweep
// re-enumerates every round — a member forking after the snapshot lands in
// the next one — and if the leaf still holds members after the cap, the
// failure is explicit instead of a false "killed": the caller's process-group
// kill, whose reuse safety is the leader-reaping discipline, takes over.
func (r *cgroupResources) kill() error {
	// A refused write to an existing cgroup.kill is a real degradation, not a
	// silent downgrade to the sweep: the cause travels with the result even
	// when the sweep drains the leaf. A missing cgroup.kill (older kernel) is
	// the documented fallback trigger, not an error.
	var cgroupKillErr error
	if r.killFile {
		err := writeCgroupKill(r.path)
		if err == nil {
			return nil
		}
		cgroupKillErr = fmt.Errorf("write cgroup.kill in %s: %w", r.path, err)
	}
	const sweepRounds = 25
	var firstErr error
	drained := false
	for range sweepRounds {
		pids, err := r.members()
		if err != nil {
			return errors.Join(cgroupKillErr, err)
		}
		if len(pids) == 0 {
			populated, err := r.populated()
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return errors.Join(cgroupKillErr, err)
			}
			if !populated {
				drained = true
				break
			}
		}
		leafRel := strings.TrimPrefix(r.path, cgroupRoot)
		for _, pid := range pids {
			if err := killMember(pid, leafRel); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return errors.Join(cgroupKillErr, firstErr)
		}
		time.Sleep(4 * time.Millisecond)
	}
	if !drained {
		firstErr = fmt.Errorf("cgroup leaf %s did not drain after %d sweep rounds; the per-PID fallback requires cgroup.kill (kernel 5.14+)", r.path, sweepRounds)
	}
	return errors.Join(cgroupKillErr, firstErr)
}

// killMember signals exactly the process that is a member of the leaf at
// verification time: pidfd_open pins the current holder of the PID, the
// membership re-check refuses a PID already handed to an unrelated process,
// and pidfd_send_signal never follows reuse after that. A membership check
// that cannot be completed is an error, not a skip: silently leaving a
// verified member alive would hang the unit wait.
func killMember(pid int, leafRel string) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return fmt.Errorf("pidfd open %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(fd) }()
	inLeaf, err := processInLeafFn(pid, leafRel)
	if err != nil {
		return fmt.Errorf("verify membership of %d: %w", pid, err)
	}
	if !inLeaf {
		// The PID left the leaf (exited, or was reused by a process that
		// never was a member): nothing to signal.
		return nil
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("pidfd signal %d: %w", pid, err)
	}
	return nil
}

// processInLeafFn is swapped in tests to exercise verification failures.
var processInLeafFn = processInLeaf

func processInLeaf(pid int, leafRel string) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The pinned process is fully gone; the pidfd would refuse the
			// signal with ESRCH anyway.
			return false, nil
		}
		return false, fmt.Errorf("read cgroup membership of %d: %w", pid, err)
	}
	want := "0::" + leafRel
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == want || strings.HasPrefix(line, want+"/") {
			return true, nil
		}
	}
	return false, nil
}

// close drains the leaf within a bounded window — members leave on exit
// before reaping — then closes the descriptor and removes the leaf. A leaf
// that still holds members is left in place and reported: procguard never
// pretends a group it could not empty was cleaned up.
func (r *cgroupResources) close() error {
	var drainErr error
	for range 50 {
		remaining, err := r.remaining()
		if err != nil {
			drainErr = err
			break
		}
		if !remaining {
			break
		}
		time.Sleep(4 * time.Millisecond)
	}
	var closeErr error
	if r.dir != nil {
		if err := r.dir.Close(); err != nil {
			closeErr = fmt.Errorf("close cgroup directory: %w", err)
		}
		r.dir = nil
	}
	if drainErr != nil {
		return errors.Join(drainErr, closeErr)
	}
	var dirs []string
	err := filepath.WalkDir(r.path, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return closeErr
	}
	if err != nil {
		return errors.Join(closeErr, err)
	}
	var removeErr error
	for _, dir := range slices.Backward(dirs) {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			removeErr = errors.Join(removeErr, err)
		}
	}
	return errors.Join(closeErr, removeErr)
}

// discard releases a leaf whose launch never produced a usable child.
func (r *cgroupResources) discard() error {
	if r == nil {
		return nil
	}
	return r.close()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
