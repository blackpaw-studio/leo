package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/spf13/cobra"
)

const (
	defaultDrainTimeout = 10 * time.Minute
	// maxDrainTimeout matches the daemon's timeout_seconds bound.
	maxDrainTimeout    = 24 * time.Hour
	drainPollInterval  = 2 * time.Second
	drainCancelTimeout = 10 * time.Second
)

// errGracefulAborted is a drain the operator abandoned at the timeout.
var errGracefulAborted = errors.New("graceful restart aborted; the daemon was not restarted and agents were told to resume")

// errDrainTimeout is a drain that outlasted --drain-timeout without --force,
// in a run with nobody to ask.
var errDrainTimeout = errors.New("timed out waiting for agents to pause")

// gracefulOptions are the restart commands' graceful-restart flags.
type gracefulOptions struct {
	graceful     bool
	noGraceful   bool
	drainTimeout time.Duration
	force        bool
}

func addGracefulFlags(cmd *cobra.Command, o *gracefulOptions) {
	f := cmd.Flags()
	f.BoolVar(&o.graceful, "graceful", false, "let busy agents pause before restarting, without asking")
	f.BoolVar(&o.noGraceful, "no-graceful", false, "restart at once even if agents are busy, without asking")
	f.DurationVar(&o.drainTimeout, "drain-timeout", defaultDrainTimeout, "how long to wait for busy agents to pause")
	f.BoolVar(&o.force, "force", false, "restart when the drain times out instead of asking or aborting")
	cmd.MarkFlagsMutuallyExclusive("graceful", "no-graceful")
}

func (o gracefulOptions) validate() error {
	if o.drainTimeout <= 0 {
		return fmt.Errorf("--drain-timeout must be positive (got %s)", o.drainTimeout)
	}
	if o.drainTimeout > maxDrainTimeout {
		return fmt.Errorf("--drain-timeout must be at most %s (got %s)", maxDrainTimeout, o.drainTimeout)
	}
	return nil
}

// explicit reports whether the operator chose a mode on the command line.
func (o gracefulOptions) explicit() bool { return o.graceful || o.noGraceful }

// drainClient is the daemon's /drain API.
type drainClient interface {
	Busy(ctx context.Context) ([]drain.Probe, error)
	Begin(ctx context.Context, timeout time.Duration) (drain.Status, error)
	Status(ctx context.Context) (drain.Status, error)
	Commit(ctx context.Context, force bool) error
	Cancel(ctx context.Context) error
}

type daemonDrainClient struct{ home string }

func (c daemonDrainClient) Busy(ctx context.Context) ([]drain.Probe, error) {
	return daemon.DrainBusy(ctx, c.home)
}

func (c daemonDrainClient) Begin(ctx context.Context, timeout time.Duration) (drain.Status, error) {
	return daemon.DrainBegin(ctx, c.home, timeout)
}

func (c daemonDrainClient) Status(ctx context.Context) (drain.Status, error) {
	return daemon.DrainStatus(ctx, c.home)
}

func (c daemonDrainClient) Commit(ctx context.Context, force bool) error {
	return daemon.DrainCommit(ctx, c.home, force)
}

func (c daemonDrainClient) Cancel(ctx context.Context) error { return daemon.DrainCancel(ctx, c.home) }

// gracefulDeps are the side effects gracefulRestart drives.
type gracefulDeps struct {
	drain drainClient
	// restart restarts the daemon and waits for it (restartDaemonAndWait).
	restart       func(ctx context.Context) error
	isInteractive func() bool
	// ask prints question and returns the operator's line, or ctx's error
	// once ctx ends (Ctrl-C never sticks in a blocking read).
	ask          func(ctx context.Context, question string) (string, error)
	clock        drain.Clock
	out          io.Writer
	pollInterval time.Duration
}

func newGracefulDeps(cfg *config.Config) gracefulDeps { return newGracefulDepsTo(cfg, os.Stdout) }

// newGracefulDepsTo is newGracefulDeps with progress written to out (stderr
// for --json commands, whose stdout carries only the result).
func newGracefulDepsTo(cfg *config.Config, out io.Writer) gracefulDeps {
	home := cfg.HomePath
	restartDeps := newDaemonRestartDeps(cfg)
	return gracefulDeps{
		drain: daemonDrainClient{home: home},
		restart: func(ctx context.Context) error {
			fmt.Fprintln(out, "Restarting daemon (waiting for agents to restore)...")
			return restartDaemonAndWait(ctx, home, restartDeps)
		},
		isInteractive: prompt.IsInteractive,
		ask:           newStdinAsker(os.Stdin, os.Stdout),
		clock:         drain.SystemClock{},
		out:           out,
		pollInterval:  drainPollInterval,
	}
}

// gracefulRestart restarts the daemon, first letting busy agents reach a
// safe stopping point when the operator opts in (see the graceful-restart
// spec). The restart error passes through unchanged, so callers keep
// treating errDaemonNotReady as a warning.
func gracefulRestart(ctx context.Context, opts gracefulOptions, d gracefulDeps) error {
	busy, err := d.drain.Busy(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return busyCheckFailed(ctx, opts, err, d)
	}
	if len(busy) == 0 {
		return d.restart(ctx)
	}
	if agents, _ := splitBusy(busy); len(agents) == 0 {
		fmt.Fprintf(d.out, "%s; they will be adopted across the restart.\n", workingSummary(busy))
		return d.restart(ctx)
	}
	drainFirst, err := chooseDrain(ctx, opts, busy, d)
	if err != nil {
		return err
	}
	if !drainFirst {
		return d.restart(ctx)
	}
	return drainThenRestart(ctx, opts, d)
}

// busyCheckFailed decides a restart whose busy check failed. Without a
// daemon, or with one that cannot drain, there is nothing to drain and the
// restart goes ahead as before. Any other failure would silently skip a
// drain that may be needed: a run that would never drain (--no-graceful,
// or unattended without a flag) and --force restart with a warning,
// --graceful aborts, and an interactive run asks.
func busyCheckFailed(ctx context.Context, opts gracefulOptions, err error, d gracefulDeps) error {
	switch {
	case errors.Is(err, daemon.ErrDrainUnsupported):
		fmt.Fprintln(d.out, "Warning: the running daemon predates graceful restart; restarting without draining.")
		return d.restart(ctx)
	case errors.Is(err, daemon.ErrDaemonUnreachable):
		fmt.Fprintln(d.out, "The daemon is not running; nothing to drain.")
		return d.restart(ctx)
	case opts.noGraceful || opts.force || (!opts.graceful && !d.isInteractive()):
		fmt.Fprintf(d.out, "Warning: could not check for busy agents (%v); restarting without draining.\n", err)
		return d.restart(ctx)
	case opts.graceful:
		return fmt.Errorf("could not check for busy agents, so they cannot be drained: %w (pass --force to restart without draining)", err)
	}
	fmt.Fprintf(d.out, "Could not check for busy agents: %v\n", err)
	for {
		answer, askErr := d.ask(ctx, "Restart without draining? Busy agents would be cut off mid-turn. [y/N] ")
		if askErr != nil {
			return askErr
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return d.restart(ctx)
		case "", "n", "no":
			return fmt.Errorf("could not check for busy agents: %w; the daemon was not restarted", err)
		}
	}
}

func chooseDrain(ctx context.Context, opts gracefulOptions, busy []drain.Probe, d gracefulDeps) (bool, error) {
	switch {
	case opts.noGraceful:
		return false, nil
	case opts.graceful:
		return true, nil
	case !d.isInteractive():
		fmt.Fprintf(d.out, "%s; restarting without draining (pass --graceful to let them pause first).\n", workingSummary(busy))
		return false, nil
	}
	for {
		answer, err := d.ask(ctx, workingSummary(busy)+". Graceful restart? [Y/n] ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// workingSummary names the busy agents, then the running dispatches.
func workingSummary(busy []drain.Probe) string {
	agents, dispatches := splitBusy(busy)
	var parts []string
	if len(agents) > 0 {
		parts = append(parts, countedNames(agents, "agent is working", "agents are working"))
	}
	if len(dispatches) > 0 {
		parts = append(parts, countedNames(dispatches, "dispatch is running", "dispatches are running"))
	}
	return strings.Join(parts, " and ")
}

// splitBusy separates busy agents from running dispatches, keeping order.
func splitBusy(busy []drain.Probe) (agents, dispatches []string) {
	for _, p := range busy {
		if p.Kind == drain.KindDispatch {
			dispatches = append(dispatches, p.Name)
		} else {
			agents = append(agents, p.Name)
		}
	}
	return agents, dispatches
}

func countedNames(names []string, one, many string) string {
	phrase := many
	if len(names) == 1 {
		phrase = one
	}
	return fmt.Sprintf("%d %s (%s)", len(names), phrase, strings.Join(names, ", "))
}

func drainThenRestart(ctx context.Context, opts gracefulOptions, d gracefulDeps) error {
	st, err := d.drain.Begin(ctx, opts.drainTimeout)
	w := &drainWatch{d: d, opts: opts, deadline: d.clock.Now().Add(opts.drainTimeout), seen: map[string]drain.MemberState{}}
	switch {
	case errors.Is(err, daemon.ErrDrainUnsupported):
		fmt.Fprintln(d.out, "Warning: the running daemon cannot drain agents; restarting without draining.")
		return d.restart(ctx)
	case errors.Is(err, drain.ErrInProgress):
		return errors.New("another graceful restart is already draining agents")
	case err != nil && ctx.Err() != nil:
		return w.interrupted(ctx)
	case err != nil:
		return fmt.Errorf("starting the drain: %w", err)
	}
	fmt.Fprintf(d.out, "Asked %d busy agent(s) to pause; waiting up to %s.\n", len(st.Members), formatDrainTimeout(opts.drainTimeout))
	return w.run(ctx, st)
}

// drainWatch follows one drain from Begin to restart, cancel or abort.
type drainWatch struct {
	d        gracefulDeps
	opts     gracefulOptions
	deadline time.Time
	seen     map[string]drain.MemberState
	warned   bool
}

func (w *drainWatch) run(ctx context.Context, st drain.Status) error {
	for {
		if ctx.Err() != nil {
			return w.interrupted(ctx)
		}
		w.report(st)
		if st.Phase == drain.PhaseIdle {
			return errors.New("the daemon ended the drain before the restart; the daemon was not restarted")
		}
		if st.Done {
			if finished, err := w.commitAndRestart(ctx, false); finished {
				return err
			}
		} else if !w.d.clock.Now().Before(w.deadline) {
			if finished, err := w.timedOut(ctx, st); finished {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return w.interrupted(ctx)
		case <-w.d.clock.After(w.d.pollInterval):
		}
		next, err := w.d.drain.Status(ctx)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(w.d.out, "Warning: polling the drain: %v\n", err)
			}
			continue
		}
		st = next
	}
}

// report prints each member's state changes and the drain's warning once.
func (w *drainWatch) report(st drain.Status) {
	if st.Warning != "" && !w.warned {
		w.warned = true
		fmt.Fprintf(w.d.out, "Warning: %s\n", st.Warning)
	}
	for _, m := range st.Members {
		if w.seen[m.Name] == m.State {
			continue
		}
		w.seen[m.Name] = m.State
		fmt.Fprintf(w.d.out, "  %s: %s\n", m.Name, m.State)
	}
}

// timedOut handles a passed deadline; finished is false to keep waiting.
func (w *drainWatch) timedOut(ctx context.Context, st drain.Status) (finished bool, err error) {
	stragglers := strings.Join(stragglerNames(st), ", ")
	fmt.Fprintf(w.d.out, "Timed out after %s waiting for: %s\n", formatDrainTimeout(w.opts.drainTimeout), stragglers)
	switch {
	case w.opts.force:
		return w.commitAndRestart(ctx, true)
	case !w.d.isInteractive():
		w.cancel(ctx)
		return true, fmt.Errorf("%w (%s); restart cancelled (pass --force to restart anyway)", errDrainTimeout, stragglers)
	}
	question := fmt.Sprintf("[r]estart anyway / [w]ait another %s / [a]bort? ", formatDrainTimeout(w.opts.drainTimeout))
	for {
		var answer string
		err := w.withHeartbeat(ctx, func() error {
			var askErr error
			answer, askErr = w.d.ask(ctx, question)
			return askErr
		})
		if err != nil {
			return true, w.interrupted(ctx)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "r", "restart":
			return w.commitAndRestart(ctx, true)
		case "w", "wait":
			w.deadline = w.d.clock.Now().Add(w.opts.drainTimeout)
			return false, nil
		case "a", "abort":
			w.cancel(ctx)
			return true, errGracefulAborted
		}
	}
}

// commitAndRestart commits the drain and restarts; finished is false when
// an agent went busy again between the last poll and the commit.
func (w *drainWatch) commitAndRestart(ctx context.Context, force bool) (finished bool, err error) {
	if err := w.d.drain.Commit(ctx, force); err != nil {
		if !force && errors.Is(err, drain.ErrNotDrained) {
			return false, nil
		}
		if ctx.Err() != nil {
			return true, w.interrupted(ctx)
		}
		w.cancel(ctx)
		return true, fmt.Errorf("committing the drain: %w", err)
	}
	err = w.withHeartbeat(ctx, func() error { return w.d.restart(ctx) })
	if err != nil && !errors.Is(err, errDaemonNotReady) {
		w.cancel(ctx)
	}
	return true, err
}

// withHeartbeat runs f while polling the drain status, so the daemon's
// lease on the drain stays renewed while f blocks.
func (w *drainWatch) withHeartbeat(ctx context.Context, f func() error) error {
	hbCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-w.d.clock.After(w.d.pollInterval):
			}
			if hbCtx.Err() != nil {
				return
			}
			_, _ = w.d.drain.Status(hbCtx)
		}
	}()
	err := f()
	stop()
	<-done
	return err
}

// cancel ends the drain on a context of its own: the caller's may already
// be cancelled (Ctrl-C), and the agents must still be told to resume.
func (w *drainWatch) cancel(ctx context.Context) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainCancelTimeout)
	defer cancel()
	if err := w.d.drain.Cancel(cctx); err != nil && !errors.Is(err, drain.ErrNoDrain) {
		fmt.Fprintf(w.d.out, "Warning: cancelling the drain: %v (the daemon ends it on its own within a minute)\n", err)
	}
}

func (w *drainWatch) interrupted(ctx context.Context) error {
	w.cancel(ctx)
	return errors.New("interrupted; restart cancelled and paused agents told to resume")
}

func stragglerNames(st drain.Status) []string {
	var names []string
	for _, m := range st.Members {
		if m.State != drain.StateDrained && m.State != drain.StateGone {
			names = append(names, m.Name)
		}
	}
	return names
}

// formatDrainTimeout drops a duration's zero trailing units: 10m, 90s.
func formatDrainTimeout(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

type askedLine struct {
	text string
	err  error
}

// newStdinAsker reads in's lines on one background goroutine, so a
// question waits on a channel and gives way to ctx instead of a read.
func newStdinAsker(in io.Reader, out io.Writer) func(context.Context, string) (string, error) {
	var once sync.Once
	lines := make(chan askedLine)
	return func(ctx context.Context, question string) (string, error) {
		once.Do(func() { go readAskedLines(in, lines) })
		_, _ = fmt.Fprint(out, question)
		select {
		case line := <-lines:
			return line.text, line.err
		case <-ctx.Done():
			fmt.Fprintln(out)
			return "", ctx.Err()
		}
	}
}

func readAskedLines(in io.Reader, lines chan<- askedLine) {
	r := bufio.NewReader(in)
	for {
		text, err := r.ReadString('\n')
		if err != nil {
			for {
				lines <- askedLine{err: fmt.Errorf("reading the answer: %w", err)}
			}
		}
		lines <- askedLine{text: text}
	}
}
