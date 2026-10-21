package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/update"
)

type handoverDirection int

const (
	handoverToApp handoverDirection = iota
	handoverToCLI
)

const (
	// handedOverSuffix names the plist the CLI install is moved to.
	handedOverSuffix = ".handed-over"
	// releasingSuffix names the marker while a release to the CLI is in flight.
	releasingSuffix = ".releasing"

	// lockWaitTimeout bounds the wait for the old daemon to drop daemon.lock
	// after its job is booted out (it drains its own shutdown first).
	lockWaitTimeout = 60 * time.Second
	lockPollEvery   = 500 * time.Millisecond
	// cliReadyTimeout bounds the wait for the restored CLI daemon.
	cliReadyTimeout = daemonReadyTimeout
)

// Stable handover steps and error codes (part of the app contract).
const (
	stepPreflight = "preflight"
	stepEnv       = "env"
	stepDrain     = "drain"
	stepBootout   = "bootout"
	stepPlist     = "plist"
	stepLock      = "lock"
	stepMarker    = "marker"
	stepBootstrap = "bootstrap"
	stepReady     = "ready"
)

type handoverOptions struct {
	label        string
	dryRun       bool
	drainTimeout time.Duration
	force        bool
}

// handoverResult is the --json object of `leo service handover`.
type handoverResult struct {
	OK             bool              `json:"ok"`
	DryRun         bool              `json:"dry_run,omitempty"`
	Step           string            `json:"step,omitempty"`
	Error          string            `json:"error,omitempty"`
	Message        string            `json:"message,omitempty"`
	PreviousLabel  string            `json:"previous_label,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	PlistBackup    string            `json:"plist_backup,omitempty"`
	RolledBack     *bool             `json:"rolled_back,omitempty"`
	ManagedStopped bool              `json:"managed_stopped,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
	Plan           []string          `json:"plan,omitempty"`
}

// handoverDeps are the side effects a handover drives. Every filesystem and
// launchd operation is a field, so tests assert the exact sequence.
type handoverDeps struct {
	supported      bool
	home           string
	prevLabel      string // the CLI install's launchd label for home
	plist          string // the CLI install's plist path
	markerPath     string
	marker         managed.State
	currentVersion string

	// daemonAlive reports whether a daemon owns daemon.lock. Lock ownership,
	// not socket health: a daemon with broken IPC still holds the lock.
	daemonAlive   func() bool
	jobLoaded     func(label string) bool
	exists        func(path string) bool
	executable    func(path string) bool
	plistEnv      func(plist string) (map[string]string, error)
	plistProgram  func(plist string) (string, error)
	binaryVersion func(bin string) (string, error)
	bootout       func(label string) error
	bootstrap     func(plist string) error
	rename        func(oldPath, newPath string) error
	remove        func(path string) error
	waitLock      func(ctx context.Context) error
	waitReady     func(ctx context.Context) error

	// drain's restart is replaced with the critical section by runHandover.
	drain gracefulDeps
}

// stepError is a failed handover step with its stable code.
type stepError struct {
	step string
	code string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.code + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func fail(step, code string, err error) *stepError {
	return &stepError{step: step, code: code, err: err}
}

func failf(step, code, format string, args ...any) *stepError {
	return fail(step, code, fmt.Errorf(format, args...))
}

func (d handoverDeps) backupPath() string { return d.plist + handedOverSuffix }

// runHandover performs one handover and reports it. The result never panics
// or returns an error: a failure is OK=false with its step and code.
func runHandover(ctx context.Context, dir handoverDirection, opts handoverOptions, d handoverDeps) handoverResult {
	res := handoverResult{PlistBackup: d.backupPath()}
	if !d.supported {
		return failed(res, failf(stepPreflight, "unsupported", "service handover needs launchd (macOS)"))
	}
	var err *stepError
	if dir == handoverToApp {
		err = handOverToApp(ctx, opts, d, &res)
	} else {
		err = handOverToCLI(ctx, opts, d, &res)
	}
	if err != nil {
		return failed(res, err)
	}
	res.OK = true
	return res
}

func failed(res handoverResult, e *stepError) handoverResult {
	res.OK = false
	res.Step, res.Error, res.Message = e.step, e.code, e.err.Error()
	return res
}

// drainThen runs critical as the "restart" of a graceful drain: agents are
// told to pause, critical runs once they have, and a failure cancels the
// drain so they resume. A drain that never completes fails with drain_timeout.
func drainThen(ctx context.Context, opts handoverOptions, d handoverDeps, critical func(context.Context) *stepError) *stepError {
	var stepErr *stepError
	deps := d.drain
	deps.isInteractive = func() bool { return false }
	deps.restart = func(ctx context.Context) error {
		stepErr = critical(ctx)
		if stepErr != nil {
			return stepErr
		}
		return nil
	}
	err := gracefulRestart(ctx, gracefulOptions{graceful: true, drainTimeout: opts.drainTimeout, force: opts.force}, deps)
	switch {
	case err == nil:
		return nil
	case stepErr != nil:
		return stepErr
	case errors.Is(err, errDrainTimeout):
		return fail(stepDrain, "drain_timeout", err)
	default:
		return fail(stepDrain, "drain_failed", err)
	}
}

func handOverToApp(ctx context.Context, opts handoverOptions, d handoverDeps, res *handoverResult) *stepError {
	res.PreviousLabel = d.prevLabel
	loaded := d.jobLoaded(d.prevLabel)
	switch {
	case d.marker.Managed:
		return failf(stepPreflight, "already_managed", "this install is already managed by %s", d.marker.Manager)
	case !managed.ValidLabel(opts.label):
		return failf(stepPreflight, "invalid_label", "--label %q must be reverse-DNS and not %s", opts.label, managed.CLILaunchdLabel)
	case opts.label == d.prevLabel:
		return failf(stepPreflight, "label_conflict", "--label must differ from the CLI install's label %s", d.prevLabel)
	case !loaded && d.daemonAlive():
		return failf(stepPreflight, "no_launchd_job", "a leo daemon is running but launchd has no job %s for it (started by hand?); stop it or register it first", d.prevLabel)
	case !d.exists(d.plist):
		return failf(stepPreflight, "plist_missing", "no launchd plist at %s", d.plist)
	}

	env, err := d.plistEnv(d.plist)
	if err != nil {
		return fail(stepEnv, "env_unreadable", err)
	}
	res.Env = env

	if opts.dryRun {
		res.DryRun = true
		if loaded {
			res.Plan = []string{"drain agents (graceful)", "launchctl bootout " + d.prevLabel}
		}
		res.Plan = append(res.Plan, "rename "+d.plist+" "+d.backupPath())
		if loaded {
			res.Plan = append(res.Plan, "wait for daemon.lock")
		}
		return nil
	}
	if !loaded {
		if err := d.rename(d.plist, d.backupPath()); err != nil {
			return fail(stepPlist, "plist_move_failed", err)
		}
		return nil
	}
	return drainThen(ctx, opts, d, func(ctx context.Context) *stepError {
		return toAppCritical(ctx, d, res)
	})
}

// toAppCritical boots the CLI job out and moves its plist aside once the
// daemon has released daemon.lock. A failure after the bootout undoes what it
// did, and says whether that worked.
func toAppCritical(ctx context.Context, d handoverDeps, res *handoverResult) *stepError {
	if err := d.bootout(d.prevLabel); err != nil {
		return fail(stepBootout, "bootout_failed", err)
	}
	if err := d.rename(d.plist, d.backupPath()); err != nil {
		res.RolledBack = ptrTo(d.bootstrap(d.plist) == nil)
		return fail(stepPlist, "plist_move_failed", err)
	}
	if err := d.waitLock(ctx); err != nil {
		ok := d.rename(d.backupPath(), d.plist) == nil && d.bootstrap(d.plist) == nil
		res.RolledBack = ptrTo(ok)
		return fail(stepLock, "lock_timeout", err)
	}
	return nil
}

func handOverToCLI(ctx context.Context, opts handoverOptions, d handoverDeps, res *handoverResult) *stepError {
	res.PreviousLabel = d.marker.LaunchdLabel
	backup := d.backupPath()
	if !d.marker.Managed {
		return failf(stepPreflight, "not_managed", "this install is not managed by Leo.app")
	}
	if !d.exists(backup) {
		return failf(stepPreflight, "backup_missing", "no handed-over plist at %s to restore", backup)
	}
	program, err := d.plistProgram(backup)
	if err != nil || !d.exists(program) || !d.executable(program) {
		return failf(stepPreflight, "backup_unusable", "the backed-up plist's leo binary %q is missing or not executable (%v)", program, err)
	}
	if v, err := d.binaryVersion(program); err == nil && versionOlder(v, d.currentVersion) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the restored leo (%s) is older than the one managing this install (%s); its on-disk state may be newer than it understands", strings.TrimSpace(v), d.currentVersion))
	}

	if opts.dryRun {
		res.DryRun = true
		res.Plan = []string{
			"drain agents (graceful)",
			"launchctl bootout " + d.marker.LaunchdLabel,
			"wait for daemon.lock",
			"rename " + d.markerPath + " " + d.markerPath + releasingSuffix,
			"rename " + backup + " " + d.plist,
			"launchctl bootstrap " + d.plist,
			"wait for the daemon to be ready",
			"remove " + d.markerPath + releasingSuffix,
		}
		return nil
	}
	return drainThen(ctx, opts, d, func(ctx context.Context) *stepError {
		return toCLICritical(ctx, d, res)
	})
}

// toCLICritical swaps the managed job for the CLI's. Once the managed job is
// booted out the CLI cannot bring it back (the app registered it), so any
// later failure sets ManagedStopped for the app to re-register, and restores
// the files and CLI job as far as they were changed.
func toCLICritical(ctx context.Context, d handoverDeps, res *handoverResult) *stepError {
	releasing := d.markerPath + releasingSuffix
	if err := d.bootout(d.marker.LaunchdLabel); err != nil {
		return fail(stepBootout, "bootout_failed", err)
	}
	res.ManagedStopped = true
	if err := d.waitLock(ctx); err != nil {
		return fail(stepLock, "lock_timeout", err)
	}
	// The marker goes aside before the CLI daemon starts: with it live, the
	// new daemon would persist the app's self_path everywhere.
	if err := d.rename(d.markerPath, releasing); err != nil {
		return fail(stepMarker, "marker_move_failed", err)
	}
	restoreMarker := func() bool { return d.rename(releasing, d.markerPath) == nil }
	if err := d.rename(d.backupPath(), d.plist); err != nil {
		res.RolledBack = ptrTo(restoreMarker())
		return fail(stepPlist, "plist_restore_failed", err)
	}
	unwind := func() {
		ok := d.bootout(d.prevLabel) == nil
		ok = d.rename(d.plist, d.backupPath()) == nil && ok
		ok = restoreMarker() && ok
		res.RolledBack = ptrTo(ok)
	}
	if err := d.bootstrap(d.plist); err != nil {
		unwind()
		return fail(stepBootstrap, "bootstrap_failed", err)
	}
	if err := d.waitReady(ctx); err != nil {
		unwind()
		return fail(stepReady, "not_ready", err)
	}
	if err := d.remove(releasing); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not remove %s: %v", releasing, err))
	}
	return nil
}

// versionOlder reports whether the version output restored is older than
// current. Unparseable output (or a dev build) never warns.
func versionOlder(restoredOutput, current string) bool {
	fields := strings.Fields(restoredOutput)
	if len(fields) == 0 || current == "" || strings.HasPrefix(current, "dev") {
		return false
	}
	restored := fields[len(fields)-1]
	if strings.HasPrefix(restored, "dev") {
		return false
	}
	return update.IsNewer(restored, current)
}

func ptrTo[T any](v T) *T { return &v }

// daemonAliveByLock reports whether a daemon holds state/daemon.lock, by trying
// to take it without blocking (and dropping it again at once). Any failure to
// take it other than contention counts as "not alive": it is a preflight
// hint, and the critical section waits on the same lock for real.
func daemonAliveByLock(stateDir string) bool {
	release, err := service.AcquireDaemonLock(stateDir)
	if err == nil {
		_ = release()
		return false
	}
	var held *service.LockHeldError
	return errors.As(err, &held)
}

// waitForLockRelease polls until acquire succeeds (then releases at once) or
// timeout passes. A held lock means the old daemon is still shutting down.
func waitForLockRelease(ctx context.Context, acquire func() (func() error, error), clock drain.Clock, timeout, every time.Duration) error {
	deadline := clock.Now().Add(timeout)
	for {
		release, err := acquire()
		if err == nil {
			return release()
		}
		var held *service.LockHeldError
		if !errors.As(err, &held) {
			return err
		}
		if !clock.Now().Before(deadline) {
			return fmt.Errorf("daemon.lock still held by pid %d after %s", held.PID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clock.After(every):
		}
	}
}

// --- cobra wiring ---

func newServiceHandoverCmd() *cobra.Command {
	var (
		toApp, toCLI bool
		label        string
		asJSON       bool
		dryRun       bool
		drainTimeout time.Duration
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "handover (--to-app --label <label> | --to-cli)",
		Short: "Hand the daemon between the CLI install and Leo.app (used by the app)",
		Long: `Move the daemon's launchd registration between this CLI install and Leo.app.

--to-app drains busy agents, boots out the CLI install's launchd job, and moves
its plist to <plist>.handed-over. The app then writes managed.json and registers
its own LaunchAgent; agents are adopted from the surviving tmux sessions.

--to-cli is the inverse: it drains through the managed daemon, boots out the
marker's job, restores the plist and bootstraps it, and removes managed.json
only once the CLI daemon is up.

A step that fails is rolled back and reported. With --json stdout carries one
object ({ok, step, error, previous_label, env, plist_backup, rolled_back,
managed_stopped, warnings}); progress goes to stderr and a failure exits
non-zero. --dry-run runs the read-only checks and prints the plan.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := handoverToApp
			if toCLI {
				dir = handoverToCLI
			}
			opts := handoverOptions{label: label, dryRun: dryRun, drainTimeout: drainTimeout, force: force}
			check := func() *jsonError {
				switch {
				case toApp == toCLI:
					return failJSON("invalid_args", errors.New("pass exactly one of --to-app (with --label) or --to-cli"))
				case toApp && label == "":
					return failJSON("invalid_args", errors.New("--to-app needs --label <launchd label>"))
				case drainTimeout <= 0 || drainTimeout > maxDrainTimeout:
					return failJSON("invalid_args", fmt.Errorf("--drain-timeout must be positive and at most %s", maxDrainTimeout))
				}
				if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
					return failJSON("not_permitted", err)
				}
				return nil
			}
			if asJSON {
				return runJSON(cmd.OutOrStdout(), func() (any, error) {
					if je := check(); je != nil {
						return nil, je
					}
					return runHandoverResult(cmd, dir, opts, true)
				})
			}
			if je := check(); je != nil {
				return errors.New(je.Message)
			}
			res, err := runHandoverResult(cmd, dir, opts, false)
			if err != nil {
				return err
			}
			printHandoverText(cmd.OutOrStdout(), res.(handoverResult))
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&toApp, "to-app", false, "hand the daemon to Leo.app")
	f.BoolVar(&toCLI, "to-cli", false, "release the daemon back to the CLI install")
	f.StringVar(&label, "label", "", "launchd label Leo.app registers (--to-app)")
	f.BoolVar(&asJSON, "json", false, "print one JSON result on stdout; progress goes to stderr")
	f.BoolVar(&dryRun, "dry-run", false, "check and print the plan without changing anything")
	f.DurationVar(&drainTimeout, "drain-timeout", defaultDrainTimeout, "how long to wait for busy agents to pause")
	f.BoolVar(&force, "force", false, "continue when the drain times out instead of failing")
	return markJSON(cmd)
}

// runHandoverResult runs the handover. In JSON mode progress goes to stderr
// and a failure carries the whole result as its payload.
func runHandoverResult(cmd *cobra.Command, dir handoverDirection, opts handoverOptions, asJSON bool) (any, error) {
	progress := cmd.OutOrStdout()
	if asJSON {
		progress = cmd.ErrOrStderr()
	}
	cfg, err := loadConfig()
	var res handoverResult
	if err != nil {
		res = failed(handoverResult{}, failf(stepPreflight, "config_error", "%v", err))
	} else {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		res = runHandover(ctx, dir, opts, newHandoverDeps(cfg, progress))
	}
	if res.OK {
		return res, nil
	}
	if asJSON {
		return nil, &jsonError{Code: res.Error, Message: res.Message, Payload: res}
	}
	return nil, errors.New(res.Message)
}

func printHandoverText(out io.Writer, res handoverResult) {
	if !res.OK {
		return
	}
	if res.DryRun {
		fmt.Fprintln(out, "Dry run: nothing changed. Planned steps:")
		for _, s := range res.Plan {
			fmt.Fprintf(out, "  - %s\n", s)
		}
	} else {
		fmt.Fprintf(out, "Handover complete (previous job: %s).\n", res.PreviousLabel)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
}

// newHandoverDeps wires the real launchd, filesystem and daemon operations.
func newHandoverDeps(cfg *config.Config, progress io.Writer) handoverDeps {
	home := cfg.HomePath
	plist := service.PlistPath(home)
	st := managedState(cfg)
	d := handoverDeps{
		supported:      runtime.GOOS == "darwin",
		home:           home,
		prevLabel:      service.DaemonLabel(home),
		plist:          plist,
		markerPath:     filepath.Join(cfg.StatePath(), managed.MarkerFile),
		marker:         st,
		currentVersion: Version,
		daemonAlive:    func() bool { return daemonAliveByLock(cfg.StatePath()) },
		jobLoaded:      service.LabelLoaded,
		exists:         func(p string) bool { _, err := os.Stat(p); return err == nil },
		executable: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.Mode().Perm()&0o111 != 0
		},
		plistEnv:      service.PlistEnv,
		plistProgram:  service.PlistProgramPath,
		binaryVersion: leoBinaryVersion,
		bootout:       service.BootoutLabel,
		bootstrap:     service.Bootstrap,
		rename:        os.Rename,
		remove:        os.Remove,
		drain:         newGracefulDepsTo(cfg, progress),
	}
	acquire := func() (func() error, error) { return service.AcquireDaemonLock(cfg.StatePath()) }
	d.waitLock = func(ctx context.Context) error {
		return waitForLockRelease(ctx, acquire, drain.SystemClock{}, lockWaitTimeout, lockPollEvery)
	}
	d.waitReady = func(ctx context.Context) error {
		return daemon.WaitReady(ctx, home, cliReadyTimeout, 0)
	}
	return d
}

// leoBinaryVersion runs `<bin> --version`.
func leoBinaryVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	return strings.TrimSpace(string(out)), err
}
