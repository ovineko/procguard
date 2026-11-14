package procguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Spec describes a single launch. Args excludes argv[0]; nil Env inherits;
// nil output writers discard. Nil Stdin disconnects input. An *os.File wires
// the child's standard input directly; any other reader is copied over a pipe
// and must eventually return EOF or an error once the child exits, because
// WaitDelay cannot interrupt a blocked Read.
// Callers retain ownership of ExtraFiles.
// GracePeriod covers the leader and descendants as one unit, starting when Stop
// or cancellation is acted on, or when a natural leader exit is observed.
// Unix sends group SIGTERM first; Windows permits voluntary exit without a signal.
// At the deadline, any live members are hard-killed.
// It defaults to ten seconds when zero; negative values are invalid.
// All supported platforms finish promptly once the unit has no live members.
// AfterStart runs synchronously once on every attempt, including validation
// failures. PID is zero if no process started. Do not block the callback on Run.
type Spec struct {
	Path            string
	Args            []string
	Dir             string
	Env             []string
	Stdin           io.Reader
	Stdout, Stderr  io.Writer
	ExtraFiles      []*os.File
	GracePeriod     time.Duration
	KillImmediately bool
	Limits          Limits
	AfterStart      func(pid int, err error)
	// Cgroup requests Linux cgroup v2 isolation for this launch. Nil keeps
	// the classic process group; a non-nil request is fail-closed — see
	// CgroupConfig. Requests on other platforms fail before launch.
	Cgroup *CgroupConfig
}

// Child owns a process and its termination unit. Its methods are concurrent-safe;
// it must be obtained from Start and must not be copied.
type Child struct {
	cmd                 *exec.Cmd
	tree                *processTree
	cleanup             cleanupTree
	started             time.Time
	grace               time.Duration
	killImmediately     bool
	mu                  sync.Mutex
	requested, finished bool
	stop                chan struct{}
	exited              chan struct{}
	done                chan struct{}
	exit                Exit
	releaseSpawn        func()
}

// Exit distinguishes process status from process-unit cleanup confirmation and diagnostics.
// Killed means a hard kill was needed for any member of the termination unit.
// Wait may take up to GracePeriod after leader exit for lingering descendants,
// plus output draining. ExitedAt records the observed leader exit, not cleanup.
type Exit struct {
	PID                 int
	Code                int
	Signal              os.Signal
	StartedAt, ExitedAt time.Time
	Requested           bool
	Killed              bool
	Cleanup             CleanupState
	Err                 error
}

// Start returns only after creation, tree setup, limits, and AfterStart finish.
// Failures retain the attempt's PID and cleanup evidence in a StartError.
// Cancellation requests Stop, including cancellation during creation.
func Start(ctx context.Context, spec Spec) (*Child, error) {
	return startWith(ctx, spec, launchHooks{})
}

type launchHooks struct {
	newTree      func(int, *cgroupResources) (*processTree, error)
	beforeResume func(*processTree) error
	resume       func(*processTree) error
	cleanup      func(cleanupTree) cleanupTree
}

func startWith(ctx context.Context, spec Spec, hooks launchHooks) (child *Child, err error) {
	pid := 0
	cleanupState := CleanupConfirmed
	defer func() {
		err = newStartError(pid, cleanupState, err)
		if spec.AfterStart != nil {
			spec.AfterStart(pid, err)
		}
	}()
	if err = validateSpec(ctx, spec); err != nil {
		return nil, err
	}
	grace := spec.GracePeriod
	if grace == 0 {
		grace = defaultGracePeriod
	}
	// Termination is driven by Stop and the grace deadline, never by exec's
	// context kill, which would signal only the leader and skip the grace period.
	//nolint:gosec // Executing the caller's chosen program is the API's purpose.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), spec.Path, spec.Args...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	cmd.Stdin = spec.Stdin
	cmd.Stdout, cmd.Stderr, cmd.ExtraFiles = spec.Stdout, spec.Stderr, spec.ExtraFiles
	if spec.KillImmediately {
		cmd.WaitDelay = waitDrainAllowance
	} else {
		cmd.WaitDelay = grace + waitDrainAllowance
	}
	// The cgroup leaf is created before the launch: clone-time placement
	// needs its descriptor, and an unsatisfiable request must fail before
	// any child code runs.
	var res *cgroupResources
	if spec.Cgroup != nil {
		if res, err = newCgroupResources(spec.Cgroup); err != nil {
			return nil, err
		}
	}
	setProcAttr(cmd, res)
	started := time.Now()
	releaseSpawn, startErr := startProcess(cmd)
	if startErr != nil {
		var discardErr error
		if res != nil {
			discardErr = res.discard()
		}
		err = errors.Join(startErr, discardErr)
		return nil, fmt.Errorf("start process: %w", err)
	}
	pid = cmd.Process.Pid
	createTree := hooks.newTree
	if createTree == nil {
		createTree = newProcessTree
	}
	tree, setupErr := createTree(pid, res)
	if setupErr != nil {
		killErr := killUnattached(cmd.Process)
		waitErr := cmd.Wait()
		releaseSpawn()
		var discardErr error
		if res != nil {
			discardErr = res.discard()
		}
		cleanupState = CleanupUnconfirmed
		err = errors.Join(setupErr, killErr, waitErr, discardErr, ErrCleanupUnconfirmed)
		return nil, err
	}
	cleanup := cleanupTree(tree)
	if hooks.cleanup != nil {
		if wrapped := hooks.cleanup(cleanup); wrapped != nil {
			cleanup = wrapped
		}
	}
	child = &Child{releaseSpawn: releaseSpawn, cmd: cmd, tree: tree, cleanup: cleanup, started: started, grace: grace, killImmediately: spec.KillImmediately, stop: make(chan struct{}), exited: make(chan struct{}), done: make(chan struct{})}
	if err = applyChildLimits(pid, spec.Limits); err != nil {
		var cleanupErr error
		cleanupState, cleanupErr = failedStartCleanup(cmd, tree, cleanup)
		releaseSpawn()
		return nil, errors.Join(err, cleanupErr)
	}
	if hooks.beforeResume != nil {
		if err = hooks.beforeResume(tree); err != nil {
			var cleanupErr error
			cleanupState, cleanupErr = failedStartCleanup(cmd, tree, cleanup)
			releaseSpawn()
			return nil, errors.Join(err, cleanupErr)
		}
	}
	if err = ctx.Err(); err != nil {
		startErr := fmt.Errorf("start context: %w", err)
		var cleanupErr error
		cleanupState, cleanupErr = failedStartCleanup(cmd, tree, cleanup)
		releaseSpawn()
		return nil, errors.Join(startErr, cleanupErr)
	}
	resume := hooks.resume
	if resume == nil {
		resume = func(tree *processTree) error { return tree.resume() }
	}
	if err = resume(tree); err != nil {
		resumeErr := fmt.Errorf("resume child: %w", err)
		var cleanupErr error
		cleanupState, cleanupErr = failedStartCleanup(cmd, tree, cleanup)
		releaseSpawn()
		return nil, errors.Join(resumeErr, cleanupErr)
	}
	go child.run(ctx)
	return child, nil
}

func failedStartCleanup(cmd *exec.Cmd, tree *processTree, cleanup cleanupTree) (CleanupState, error) {
	killErr := cleanup.kill()
	leaderErr := tree.waitExited()
	_, members, cleanupErr := cleanProcessTree(cleanup, 0, true, time.Now(), time.Time{}, true, killErr)
	waitErr := cmd.Wait()
	closeErr := tree.close()
	state := members
	if leaderErr != nil || cmd.ProcessState == nil {
		state = CleanupUnconfirmed
	}
	joined := errors.Join(leaderErr, cleanupErr, waitErr, closeErr)
	if state != CleanupConfirmed {
		joined = errors.Join(joined, ErrCleanupUnconfirmed)
	}
	return state, joined
}

// PID remains available after Wait.
func (c *Child) PID() int { return c.cmd.Process.Pid }

// StartedAt is captured immediately before the successful OS creation attempt.
func (c *Child) StartedAt() time.Time { return c.started }

// Signal targets only the leader, so diagnostics cannot reach grandchildren.
func (c *Child) Signal(sig os.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return os.ErrProcessDone
	}
	if err := c.cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("signal child: %w", err)
	}
	return nil
}

// Stop requests termination without blocking; Wait and Exit.Cleanup report its outcome.
func (c *Child) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.requested && !c.finished {
		c.requested = true
		close(c.stop)
	}
}

// Wait joins lifecycle work and output draining and returns the same result to every caller.
// Inspect Exit.Cleanup: lifecycle completion alone does not establish unit cleanup.
func (c *Child) Wait() Exit { <-c.done; return c.exit }

// Done closes after cleanup attempts, leader reaping, and output draining finish.
// It does not by itself confirm that the termination unit is empty.
func (c *Child) Done() <-chan struct{} { return c.done }

// Exited closes as soon as the leader is observed to have exited, before
// descendant cleanup and output draining, which can take up to GracePeriod.
// Use it, not Done, to answer "is the process still running".
func (c *Child) Exited() <-chan struct{} { return c.exited }

// Runtime excludes descendant cleanup and output draining after observed leader exit.
func (e Exit) Runtime() time.Duration { return e.ExitedAt.Sub(e.StartedAt) }

func validateSpec(ctx context.Context, spec Spec) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start context: %w", err)
	}
	if spec.Path == "" {
		return errors.New("process path is required")
	}
	if spec.GracePeriod < 0 {
		return errors.New("grace period must not be negative")
	}
	if err := spec.Limits.validate(); err != nil {
		return err
	}
	if spec.Cgroup != nil {
		if err := validateCgroupSpec(spec.Cgroup); err != nil {
			return err
		}
	}
	return validatePlatformSpec(spec)
}

type exitObservation struct {
	at  time.Time
	err error
}

func (c *Child) run(ctx context.Context) {
	observed := make(chan exitObservation, 1)
	go func() {
		err := c.tree.waitExited()
		at := time.Now()
		if err == nil {
			close(c.exited)
		}
		observed <- exitObservation{at: at, err: err}
	}()
	var leader exitObservation
	var deadline time.Time
	var killed bool
	var terminateErr error
	select {
	case leader = <-observed:
	case <-ctx.Done():
		c.Stop()
		leader, deadline, killed, terminateErr = c.terminate(observed)
	case <-c.stop:
		leader, deadline, killed, terminateErr = c.terminate(observed)
	}
	killed, treeState, cleanupErr := c.finishTree(leader.at, deadline, killed, terminateErr)
	c.mu.Lock()
	c.finished = true
	requested := c.requested
	c.mu.Unlock()
	waitErr := c.cmd.Wait()
	closeErr := c.tree.close()
	var statusErr *exec.ExitError
	if errors.As(waitErr, &statusErr) {
		waitErr = nil
	}
	code := -1
	if c.cmd.ProcessState != nil {
		code = c.cmd.ProcessState.ExitCode()
	}
	cleanupState := treeState
	if leader.err != nil || c.cmd.ProcessState == nil {
		cleanupState = CleanupUnconfirmed
	}
	err := errors.Join(leader.err, terminateErr, cleanupErr, waitErr, closeErr)
	if cleanupState != CleanupConfirmed {
		err = errors.Join(err, ErrCleanupUnconfirmed)
	}
	c.exit = Exit{
		PID: c.PID(), Code: code, Signal: exitSignal(c.cmd.ProcessState),
		StartedAt: c.started, ExitedAt: leader.at, Requested: requested, Killed: killed,
		Cleanup: cleanupState, Err: err,
	}
	c.releaseSpawn()
	close(c.done)
}

func (c *Child) terminate(observed <-chan exitObservation) (exitObservation, time.Time, bool, error) {
	if c.killImmediately {
		err := c.cleanup.kill()
		return <-observed, time.Time{}, true, err
	}
	deadline := time.Now().Add(c.grace)
	politeErr := c.cleanup.terminate()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case leader := <-observed:
		return leader, deadline, false, politeErr
	case <-timer.C:
		select {
		case leader := <-observed:
			return leader, deadline, false, politeErr
		default:
		}
		err := c.cleanup.kill()
		return <-observed, deadline, true, errors.Join(politeErr, err)
	}
}

type cleanupTree interface {
	remaining() (bool, error)
	terminate() error
	kill() error
}

func (c *Child) finishTree(exited, deadline time.Time, killed bool, escalateErr error) (bool, CleanupState, error) {
	return cleanProcessTree(c.cleanup, c.grace, c.killImmediately, exited, deadline, killed, escalateErr)
}

func cleanImmediately(tree cleanupTree, remaining bool, probeErr error, killed bool, escalateErr error) (bool, CleanupState, error) {
	if probeErr == nil && !remaining {
		return killed, CleanupConfirmed, escalateErr
	}
	if !killed {
		escalateErr = errors.Join(escalateErr, tree.kill())
	}
	probeErrors := probeErr
	var drainDeadline time.Time
	if escalateErr != nil || probeErr != nil {
		drainDeadline = time.Now().Add(escalationDrainWindow)
	}
	ticker := time.NewTicker(treePollInterval)
	defer ticker.Stop()
	for {
		if !drainDeadline.IsZero() && !time.Now().Before(drainDeadline) {
			return true, CleanupUnconfirmed, errors.Join(probeErrors, escalateErr)
		}
		<-ticker.C
		remaining, probeErr = tree.remaining()
		if probeErr != nil && !errors.Is(probeErrors, probeErr) {
			probeErrors = errors.Join(probeErrors, probeErr)
		}
		if probeErr == nil && !remaining {
			return true, CleanupConfirmed, errors.Join(probeErrors, escalateErr)
		}
		if probeErr != nil && drainDeadline.IsZero() {
			drainDeadline = time.Now().Add(escalationDrainWindow)
		}
	}
}

// cleanProcessTree empties the termination unit. A failed escalation
// (escalateErr) must end the wait with that error instead of extending it:
// members a kill could not reach would otherwise hold Wait forever. The
// wait after a failed escalation is bounded; members that drain within it
// still produce a clean exit.
func cleanProcessTree(tree cleanupTree, grace time.Duration, immediate bool, exited, deadline time.Time, killed bool, escalateErr error) (bool, CleanupState, error) {
	remaining, probeErr := tree.remaining()
	if immediate {
		return cleanImmediately(tree, remaining, probeErr, killed, escalateErr)
	}
	if probeErr == nil && !remaining {
		return killed, CleanupConfirmed, escalateErr
	}
	var politeErr error
	if !killed && deadline.IsZero() {
		deadline = exited.Add(grace)
		politeErr = tree.terminate()
	}
	var drainDeadline time.Time
	if killed && (escalateErr != nil || probeErr != nil) {
		drainDeadline = time.Now().Add(escalationDrainWindow)
	}
	ticker := time.NewTicker(treePollInterval)
	defer ticker.Stop()
	for {
		if !killed && !time.Now().Before(deadline) {
			killErr := tree.kill()
			killed = true
			escalateErr = errors.Join(escalateErr, killErr)
			if killErr != nil || probeErr != nil {
				drainDeadline = time.Now().Add(escalationDrainWindow)
			}
		}
		if cleanupWaitDone(killed, escalateErr, probeErr, drainDeadline) {
			return killed, CleanupUnconfirmed, errors.Join(escalateErr, probeErr, politeErr)
		}
		<-ticker.C
		var err error
		remaining, err = tree.remaining()
		if err != nil {
			if !errors.Is(probeErr, err) {
				probeErr = errors.Join(probeErr, err)
			}
			if killed && drainDeadline.IsZero() {
				drainDeadline = time.Now().Add(escalationDrainWindow)
			}
		} else if !remaining {
			return killed, CleanupConfirmed, errors.Join(escalateErr, probeErr, politeErr)
		}
	}
}

func cleanupWaitDone(killed bool, escalateErr, lastProbeErr error, drainDeadline time.Time) bool {
	if !killed {
		return false
	}
	return (escalateErr != nil || lastProbeErr != nil) && !time.Now().Before(drainDeadline)
}
