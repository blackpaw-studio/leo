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
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const interactiveCommandTimeout = 5 * time.Second
const interactiveWaitDelay = 100 * time.Millisecond

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
	if p, ok := h.(harness.InteractivePreparer); ok {
		if err := p.PrepareInteractive(cfg.HomePath); err != nil {
			return "", "", err
		}
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
	return tmux.InjectInto(ctx, r.tmuxPath, paneID, r.classifier(paneID), text, arm)
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
	cmd := r.ExecCommandContext(ctx, r.tmuxPath, tmux.Args(args...)...)
	cmd.WaitDelay = interactiveWaitDelay
	return cmd
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
