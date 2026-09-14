package consult

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const interactiveCommandTimeout = 5 * time.Second
const interactiveWaitDelay = 100 * time.Millisecond
const defaultStartupTimeout = 60 * time.Second
const defaultStartupPollInterval = 500 * time.Millisecond

// ErrNotReady means a new harness pane did not render an empty composer before
// the bounded opening-injection wait elapsed.
type ErrNotReady struct{ Screen string }

func (e *ErrNotReady) Error() string {
	if e.Screen == "" {
		return "interactive composer not ready"
	}
	return "interactive composer not ready: " + e.Screen
}

// TmuxInteractiveRuntime is the deliberately small bridge between the
// dispatch state machine and a harness TUI in Leo's tmux server.
type TmuxInteractiveRuntime struct {
	configPath           string
	config               func() (*config.Config, error)
	resolveCallerSession func(string) (string, bool)
	tmuxPath, leoPath    string
	AgentToken           string
	ExecCommandContext   func(context.Context, string, ...string) *exec.Cmd
	Timeout              time.Duration
	StartupTimeout       time.Duration
	StartupPollInterval  time.Duration
	mu                   sync.RWMutex
	classifiers          map[string]tmux.ComposerClassifier
}

func NewInteractiveRuntime(cfgPath string, cfg func() (*config.Config, error), resolveCallerSession func(caller string) (session string, ok bool), tmuxPath, leoPath string) *TmuxInteractiveRuntime {
	return &TmuxInteractiveRuntime{configPath: cfgPath, config: cfg, resolveCallerSession: resolveCallerSession, tmuxPath: tmuxPath, leoPath: leoPath, ExecCommandContext: exec.CommandContext, Timeout: interactiveCommandTimeout}
}

func (r *TmuxInteractiveRuntime) Launch(ctx context.Context, req LaunchRequest) (string, string, error) {
	cfg, err := r.config()
	if err != nil {
		return "", "", fmt.Errorf("load config: %w", err)
	}
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		return "", "", fmt.Errorf("unknown template %q", req.Template)
	}
	if req.Model == "" {
		req.Model = cfg.TemplateModel(tmpl)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return "", "", err
	}
	opts, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return "", "", err
	}
	spec := harness.LaunchSpec{Kind: harness.KindAgent, Name: req.Name, Model: req.Model, MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Cwd, Options: opts}
	args, err := h.Args(spec)
	if err != nil {
		return "", "", err
	}
	hooker, ok := h.(harness.TurnHooker)
	if !ok {
		return "", "", fmt.Errorf("harness %q does not support interactive turn hooks", h.Name())
	}
	hooks, err := hooker.TurnHooks([]string{r.leoPath, "--config", r.configPath, "dispatch", "report"})
	if err != nil {
		return "", "", err
	}
	args = append(args, hooks...)
	env, err := h.Env(spec)
	if err != nil {
		return "", "", err
	}
	if env == nil {
		env = map[string]string{}
	}
	for k, v := range tmpl.Env {
		env[k] = v
	}
	if p, ok := h.(harness.InteractivePreparer); ok {
		home := cfg.HomePath
		if h.Name() == "codex" {
			home = codex.CodexHome(env)
		}
		if err := p.PrepareInteractive(home); err != nil {
			return "", "", err
		}
	}
	if h.Name() == "codex" {
		if err := codex.EnsureWorkspaceTrusted(codex.CodexHome(env), req.Cwd); err != nil {
			return "", "", err
		}
	}
	env["LEO_DISPATCH_ID"], env["LEO_CONFIG"] = req.ID, r.configPath
	if r.AgentToken != "" {
		env["LEO_API_TOKEN"] = r.AgentToken
	}
	session := ""
	if r.resolveCallerSession != nil && req.Caller != "" {
		if candidate, live := r.resolveCallerSession(req.Caller); live && r.run(ctx, "has-session", "-t", tmux.Target(candidate)) == nil {
			session = candidate
		}
	}
	if session == "" {
		session = dispatchViewerSession
		if r.run(ctx, "has-session", "-t", tmux.Target(session)) != nil && r.run(ctx, "new-session", "-d", "-s", session) != nil && r.run(ctx, "has-session", "-t", tmux.Target(session)) != nil {
			return "", "", fmt.Errorf("create fallback tmux session %q", session)
		}
	}
	label := viewerWindowName(Record{ID: req.ID, Name: req.Name, Template: req.Template})
	command := make([]string, 0, len(args)+1)
	command = append(command, h.Binary())
	command = append(command, args...)
	words := make([]string, len(command))
	for i, word := range command {
		words[i] = shellQuote(word)
	}
	argv := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.Target(session), "-n", label, "-c", req.Cwd}
	for _, k := range sortedKeys(env) {
		argv = append(argv, "-e", k+"="+env[k])
	}
	argv = append(argv, strings.Join(words, " "))
	out, err := r.output(ctx, argv...)
	if err != nil {
		return "", "", fmt.Errorf("launch interactive pane: %w", err)
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		return "", "", fmt.Errorf("tmux returned no pane id")
	}
	r.mu.Lock()
	if r.classifiers == nil {
		r.classifiers = make(map[string]tmux.ComposerClassifier)
	}
	if h.Name() == "codex" {
		r.classifiers[pane] = tmux.CodexComposerClassifier
	} else {
		r.classifiers[pane] = tmux.ClaudeComposerClassifier
	}
	r.mu.Unlock()
	return pane, label, nil
}

func (r *TmuxInteractiveRuntime) Inject(ctx context.Context, paneID, text string, arm func()) error {
	return r.inject(ctx, paneID, text, arm)
}

// InjectOpening waits for a fresh harness pane to expose an empty composer.
// Follow-up sends use Inject, which intentionally remains single-shot.
func (r *TmuxInteractiveRuntime) InjectOpening(ctx context.Context, paneID, text string, arm func()) error {
	timeout := r.StartupTimeout
	if timeout <= 0 {
		timeout = defaultStartupTimeout
	}
	poll := r.StartupPollInterval
	if poll <= 0 {
		poll = defaultStartupPollInterval
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		capture, err := r.output(readyCtx, "capture-pane", "-p", "-t", paneID)
		if err == nil {
			last = string(capture)
			if r.classifier(paneID)(last) == tmux.ComposerEmpty {
				return r.inject(ctx, paneID, text, arm)
			}
		}
		wait := time.NewTimer(poll)
		select {
		case <-readyCtx.Done():
			wait.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &ErrNotReady{Screen: lastNonEmptyLines(last, 3)}
		case <-wait.C:
		}
	}
}

func (r *TmuxInteractiveRuntime) inject(ctx context.Context, paneID, text string, arm func()) error {
	command := func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return r.commandArgs(ctx, args...)
	}
	return tmux.InjectIntoWith(ctx, r.tmuxPath, paneID, r.classifier(paneID), text, arm, tmux.CommandFunc(command))
}
func (r *TmuxInteractiveRuntime) Alive(paneID string) bool {
	out, err := r.output(context.Background(), "display-message", "-p", "-t", paneID, "#{pane_dead}")
	return err == nil && strings.TrimSpace(string(out)) == "0"
}
func (r *TmuxInteractiveRuntime) Kill(paneID string) error {
	return r.run(context.Background(), "kill-pane", "-t", paneID)
}
func (r *TmuxInteractiveRuntime) ComposerEmpty(paneID string) bool {
	out, err := r.output(context.Background(), "capture-pane", "-p", "-t", paneID)
	return err == nil && r.classifier(paneID)(string(out)) == tmux.ComposerEmpty
}
func (r *TmuxInteractiveRuntime) classifier(pane string) tmux.ComposerClassifier {
	r.mu.RLock()
	classify := r.classifiers[pane]
	r.mu.RUnlock()
	if classify == nil {
		return tmux.ClaudeComposerClassifier
	}
	return classify
}
func (r *TmuxInteractiveRuntime) command(ctx context.Context, args ...string) *exec.Cmd {
	return r.commandArgs(ctx, tmux.Args(args...)...)
}

func (r *TmuxInteractiveRuntime) commandArgs(ctx context.Context, args ...string) *exec.Cmd {
	cmd := r.ExecCommandContext(ctx, r.tmuxPath, args...)
	cmd.WaitDelay = interactiveWaitDelay
	return cmd
}

func lastNonEmptyLines(capture string, limit int) string {
	lines := strings.Split(capture, "\n")
	nonEmpty := make([]string, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(nonEmpty) < limit; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	for left, right := 0, len(nonEmpty)-1; left < right; left, right = left+1, right-1 {
		nonEmpty[left], nonEmpty[right] = nonEmpty[right], nonEmpty[left]
	}
	return strings.Join(nonEmpty, "\n")
}
func (r *TmuxInteractiveRuntime) run(ctx context.Context, args ...string) error {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = interactiveCommandTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.command(cctx, args...).Run()
}
func (r *TmuxInteractiveRuntime) output(ctx context.Context, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = interactiveCommandTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.command(cctx, args...).Output()
}
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
