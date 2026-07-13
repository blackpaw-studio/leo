// Package tmuxtui is the shared session driver for every harness that runs
// its interactive TUI as the supervised process inside a leo tmux session
// (claude, codex, opencode). Per-harness differences ride in through Config:
// the readiness-probe profile, dialog policy, quick-exit recovery, workspace
// pre-launch hook, session-args refresh, and post-hoc session-id discovery.
package tmuxtui

import (
	"context"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// Seams tests replace; production uses the real tmux helpers.
var (
	injectPromptFn = tmux.InjectPromptTUI
	locateTmuxFn   = tmux.Locate
)

// discoverPoll/discoverBudget bound the post-launch session-id discovery
// loop. Vars so tests shrink them. The budget is generous because a session
// id only exists after the FIRST turn runs — a process with no opening
// prompt may idle unbounded before its first injected message; Inject
// re-arms discovery in that case, so a Start-loop miss is not fatal.
var (
	discoverPoll   = 2 * time.Second
	discoverBudget = 5 * time.Minute
)

// discovering dedupes in-flight discovery loops per tmux session.
var discovering sync.Map // TmuxSession → struct{}

type Config struct {
	Probe         tmux.Profile
	PaneKeyFn     func(pane string) string
	RecoverFn     func(args []string) ([]string, harness.QuickExitAction)
	PreLaunchFn   func(h harness.SessionHandle) error
	RefreshArgsFn func(args []string, storedID string) []string
	DiscoverIDFn  func(ctx context.Context, h harness.SessionHandle, since time.Time) (string, error)
}

type Driver struct{ cfg Config }

func New(cfg Config) Driver { return Driver{cfg: cfg} }

// Start delivers the opening prompt (once, guarded by the empty IDs store so
// restarts never replay it) and arms session-id discovery for harnesses that
// can't pin an id at launch.
func (d Driver) Start(ctx context.Context, h harness.SessionHandle) error {
	since := time.Now().Add(-2 * time.Minute) // slack: spawn preceded Start
	if h.OpeningPrompt != "" && h.IDs.Get() == "" {
		if _, err := d.Inject(ctx, h, h.OpeningPrompt); err != nil {
			return err
		}
	}
	d.maybeDiscover(ctx, h, since)
	return nil
}

// Inject pastes msg into the live TUI (readiness-probed). Delivery is
// asynchronous — the turn outcome lives in the pane — so Result is always
// nil (claude parity for every harness).
func (d Driver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return nil, err
	}
	if err := injectPromptFn(ctx, tmuxPath, h.TmuxSession, msg, d.cfg.Probe); err != nil {
		return nil, err
	}
	d.maybeDiscover(ctx, h, time.Now().Add(-2*time.Minute))
	return nil, nil
}

// maybeDiscover starts one background discovery loop when the harness needs
// post-hoc id discovery and no id is stored yet. Deduped per tmux session.
func (d Driver) maybeDiscover(ctx context.Context, h harness.SessionHandle, since time.Time) {
	if d.cfg.DiscoverIDFn == nil || h.IDs.Get() != "" {
		return
	}
	if _, loaded := discovering.LoadOrStore(h.TmuxSession, struct{}{}); loaded {
		return
	}
	go func() {
		defer discovering.Delete(h.TmuxSession)
		deadline := time.Now().Add(discoverBudget)
		for {
			if id, err := d.cfg.DiscoverIDFn(ctx, h, since); err == nil && id != "" {
				h.IDs.Set(id)
				return
			}
			if time.Now().After(deadline) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(discoverPoll):
			}
		}
	}()
}

func (d Driver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{TmuxSession: h.TmuxSession}, nil
}

// AbortTurn cancels a mid-turn TUI by sending Escape then Ctrl-C — the same
// keys for every harness.
func (d Driver) AbortTurn(h harness.SessionHandle) error {
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return err
	}
	return tmux.AbortPrompt(context.Background(), tmuxPath, h.TmuxSession)
}

func (d Driver) PaneKey(pane string) string {
	if d.cfg.PaneKeyFn == nil {
		return ""
	}
	return d.cfg.PaneKeyFn(pane)
}

func (d Driver) RecoverQuickExit(args []string) ([]string, harness.QuickExitAction) {
	if d.cfg.RecoverFn == nil {
		return args, harness.QuickExitClearSession
	}
	return d.cfg.RecoverFn(args)
}

func (d Driver) PreLaunch(h harness.SessionHandle) error {
	if d.cfg.PreLaunchFn == nil {
		return nil
	}
	return d.cfg.PreLaunchFn(h)
}

func (d Driver) RefreshSessionArgs(args []string, storedID string) []string {
	if d.cfg.RefreshArgsFn == nil {
		return args
	}
	return d.cfg.RefreshArgsFn(args, storedID)
}

// SetInjectPromptForTest / SetLocateTmuxForTest swap the seams and return a
// restore func (same convention as the claude driver's former seams).
func SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string, p tmux.Profile) error) func() {
	prev := injectPromptFn
	injectPromptFn = fn
	return func() { injectPromptFn = prev }
}

func SetLocateTmuxForTest(fn func() (string, error)) func() {
	prev := locateTmuxFn
	locateTmuxFn = fn
	return func() { locateTmuxFn = prev }
}
