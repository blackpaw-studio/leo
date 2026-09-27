package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const interactiveCommandTimeout = 5 * time.Second
const interactiveWaitDelay = 100 * time.Millisecond
const defaultStartupTimeout = 60 * time.Second
const defaultStartupPollInterval = 500 * time.Millisecond

// claudeArgvPromptLimit is a sanity cap on the opening brief's size, measured
// on the preamble-wrapped bytes. The brief itself never appears as literal
// launch argv text (see claudeBriefArgvWord), sidestepping tmux 3.6a's own
// ~16 KiB total client-command limit (measured: a 15 KiB literal argument to
// `tmux new-session -d "<cmd>"` launched, a 17 KiB one failed with "command
// too long") — but the $(cat ...) substitution's *result* still becomes a
// single argv string in the exec'd claude process, and Linux caps any one
// argv/envp string at MAX_ARG_STRLEN, 32 pages (128 KiB on the common 4 KiB
// page size), independent of the much larger overall ARG_MAX. A brief between
// this cap and the old 256 KiB would launch fine on macOS but fail with
// E2BIG on Linux. 96 KiB stays safely under MAX_ARG_STRLEN with headroom for
// the shell's own overhead in materializing the substitution; above it, the
// opening turn falls back to the existing tmux-paste injection instead.
const claudeArgvPromptLimit = 96 * 1024

// claudeDeliversPromptViaArgv reports whether an opening brief of this size
// is delivered via claude's launch-time argv (as a $(cat <brief file>)
// command substitution — see claudeBriefArgvWord) rather than a tmux paste.
// Both interactive_runtime.go (building the launch spec) and interactive.go
// (arming turn 1 before Launch, and later deciding whether injectOpening has
// anything left to do) must agree on this, so it is one pure function shared
// by both.
func claudeDeliversPromptViaArgv(harnessName, prompt string) bool {
	return harnessName == "claude" && len(prompt) <= claudeArgvPromptLimit
}

// claudeBriefArgvWord is the shell text placed in claude's launch-argv
// positional slot when claudeDeliversPromptViaArgv is true. It expands to
// path's full contents via command substitution instead of embedding them
// directly, so the tmux client command that carries the launch stays a few
// dozen bytes regardless of the brief's actual size (see
// claudeArgvPromptLimit). It deliberately bypasses the per-word shellQuote
// pass in Launch (single quotes would disable the "$(...)" expansion
// entirely): the outer double quotes keep the substituted content as one
// argv word even when it contains spaces or newlines, and the inner
// shellQuote(path) keeps a path containing a quote or a space safe.
func claudeBriefArgvWord(path string) string {
	return `"$(cat ` + shellQuote(path) + `)"`
}

// briefSpillDir names the directory (under a leo home's state dir) holding
// per-dispatch opening-brief files: <home>/state/dispatch-briefs/<id>.txt.
const briefSpillDir = "dispatch-briefs"

// dispatchBriefPath is dispatch id's opening-brief file path ("" if
// unusable).
func dispatchBriefPath(homePath, id string) string {
	if homePath == "" || id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return ""
	}
	return filepath.Join(homePath, "state", briefSpillDir, id+".txt")
}

// writePrivateBrief writes brief to path, creating its directory as needed.
// The brief can carry orchestrator- or user-authored task instructions, so
// both the directory and the file are owner-only.
func writePrivateBrief(path, brief string) error {
	if path == "" {
		return errors.New("no opening-brief path available")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating opening-brief directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone (a prior leo
	// version, or something else entirely, could have created it looser);
	// tighten it explicitly rather than trusting whatever created it first.
	// #nosec G302 -- a directory needs the owner execute bit; 0700 is owner-only
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("securing opening-brief directory: %w", err)
	}
	// A stale file (leftover from a crashed prior run) or a symlink planted
	// at this exact path must never be reused or followed: os.WriteFile
	// would keep a stale file's existing mode, and would happily write
	// through a symlink to wherever it points. Remove whatever is there
	// (Remove itself never follows a symlink) and create fresh with
	// O_EXCL|O_NOFOLLOW, so even something recreated in the gap between the
	// Remove and this Open is refused rather than silently written to.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale opening-brief file: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("creating opening-brief file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(brief)); err != nil {
		return fmt.Errorf("writing opening brief: %w", err)
	}
	return nil
}

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
	placements           map[string]string
}

type PanePresence uint8

const (
	PaneAbsent PanePresence = iota
	PanePresentAlive
	PanePresentDead
)

type panePresenceRuntime interface {
	PanePresence(string) (PanePresence, error)
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
	if claudeOpts, ok := opts.(claudeharness.Options); ok && req.Dispatched {
		opts = resolveClaudeDispatchProfile(cfg, tmpl, "dispatch", claudeOpts, tmpl.Env)
	}
	spec := harness.LaunchSpec{Kind: harness.KindAgent, Name: req.Name, Model: req.Model, Effort: req.Effort, MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Cwd, Options: opts, Dispatched: req.Dispatched}
	// Claude delivers the opening brief as a launch-time argv positional
	// instead of a tmux paste: a pasted brief arrives wrapped as
	// <pasted_content>, which the model can refuse as untrusted (see
	// injectOpening in interactive.go). Other harnesses, and an oversized
	// claude brief, keep injecting the opening turn once the pane's composer
	// is ready. spec.Prompt is deliberately left unset here: the brief goes
	// to a private file instead, and the launch command below embeds only a
	// $(cat ...) command substitution referencing it (see
	// claudeArgvPromptLimit for why).
	var briefPath string
	if claudeDeliversPromptViaArgv(h.Name(), req.Prompt) {
		briefPath = dispatchBriefPath(cfg.HomePath, req.ID)
		if err := writePrivateBrief(briefPath, req.Prompt); err != nil {
			return "", "", fmt.Errorf("claude: %w", err)
		}
	}
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
	args, err = claudeharness.MergeSettingsArgs(args, hooks, claudeharness.MergeOptions{BaseDir: req.Cwd, SpillPath: dispatchSpillPath(cfg.HomePath, req.ID)})
	if err != nil {
		return "", "", err
	}
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
			// Export the resolved home so the TUI and the prepared
			// hooks/trust files agree even when HOME is overridden.
			env["CODEX_HOME"] = home
		} else if h.Name() == "claude" {
			home = env["HOME"]
			if home == "" {
				home, err = os.UserHomeDir()
				if err != nil {
					return "", "", fmt.Errorf("resolve Claude home: %w", err)
				}
			}
		}
		if err := p.PrepareInteractive(home, req.Cwd); err != nil {
			return "", "", err
		}
	}
	if h.Name() == "codex" {
		if err := codex.EnsureWorkspaceTrusted(codex.CodexHome(env), req.Cwd); err != nil {
			return "", "", err
		}
	}
	env[dispatchIDEnv], env["LEO_CONFIG"] = req.ID, r.configPath
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
	command := make([]string, 0, len(args)+3)
	command = append(command, "env", dispatchIDEnv+"="+req.ID, h.Binary())
	command = append(command, args...)
	words := make([]string, len(command))
	for i, word := range command {
		words[i] = shellQuote(word)
	}
	if briefPath != "" {
		// Appended after the per-word shellQuote pass on purpose: shellQuote
		// wraps its input in single quotes, which would disable the
		// "$(...)" expansion this word depends on (see claudeBriefArgvWord).
		words = append(words, claudeBriefArgvWord(briefPath))
	}
	argv := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.Target(session), "-n", label, "-c", req.Cwd}
	if req.Placement.Kind == "split" {
		argv = []string{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", req.Placement.Target, "-c", req.Cwd}
		// See consult.Viewer.openSplit: a pane-scoped remain-on-exit set
		// after split-window races a fast-exiting process closing the pane
		// first. Cover the gap at window scope, then narrow to the new
		// pane and unset the window-level option once it is in place.
		_ = r.run(ctx, "set-window-option", "-t", req.CallerWindowID, "remain-on-exit", "on")
	}
	for _, k := range sortedKeys(env) {
		argv = append(argv, "-e", k+"="+env[k])
	}
	argv = append(argv, strings.Join(words, " "))
	out, err := r.output(ctx, argv...)
	if err != nil && req.Placement.Kind == "split" {
		// The split attempt already set window-scoped remain-on-exit; the
		// fallback below no longer runs the "== split" cleanup block, so
		// unset it here to avoid leaving it on the caller's window.
		_ = r.run(ctx, "set-window-option", "-u", "-t", req.CallerWindowID, "remain-on-exit")
		argv = []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.Target(session), "-n", label, "-c", req.Cwd}
		for _, k := range sortedKeys(env) {
			argv = append(argv, "-e", k+"="+env[k])
		}
		argv = append(argv, strings.Join(words, " "))
		out, err = r.output(ctx, argv...)
		if err == nil {
			req.Placement.Kind = "window"
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("launch interactive pane: %w", err)
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		if req.Placement.Kind == "split" {
			_ = r.run(ctx, "set-window-option", "-u", "-t", req.CallerWindowID, "remain-on-exit")
		}
		return "", "", fmt.Errorf("tmux returned no pane id")
	}
	if req.Placement.Kind == "split" {
		_ = r.run(ctx, "select-pane", "-t", pane, "-T", label)
		_ = r.run(ctx, "set-option", "-p", "-t", pane, "remain-on-exit", "on")
		_ = r.run(ctx, "set-window-option", "-u", "-t", req.CallerWindowID, "remain-on-exit")
		_ = r.run(ctx, "set-option", "-w", "-t", req.CallerWindowID, "main-pane-height", fmt.Sprintf("%d%%", req.Placement.MainPaneHeight))
		_ = r.run(ctx, "select-layout", "-t", req.CallerWindowID, "main-horizontal")
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
	if r.placements == nil {
		r.placements = make(map[string]string)
	}
	r.placements[pane] = req.Placement.Kind
	r.mu.Unlock()
	return pane, label, nil
}

func (r *TmuxInteractiveRuntime) ViewerKind(pane string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.placements[pane]
}

func (r *TmuxInteractiveRuntime) Inject(ctx context.Context, paneID, text string, arm func() error) error {
	return r.inject(ctx, paneID, text, arm)
}

// InjectOpening waits for a fresh harness pane to expose an empty composer.
// Follow-up sends use Inject, which intentionally remains single-shot.
func (r *TmuxInteractiveRuntime) InjectOpening(ctx context.Context, paneID, text string, arm func() error) error {
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

func (r *TmuxInteractiveRuntime) inject(ctx context.Context, paneID, text string, arm func() error) error {
	command := func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return r.commandArgs(ctx, args...)
	}
	return tmux.InjectIntoWith(ctx, r.tmuxPath, paneID, r.classifier(paneID), text, arm, tmux.CommandFunc(command))
}
func (r *TmuxInteractiveRuntime) Alive(paneID string) bool {
	presence, _ := r.PanePresence(paneID)
	return presence == PanePresentAlive
}
func (r *TmuxInteractiveRuntime) PaneAlive(paneID string) (bool, error) {
	presence, err := r.PanePresence(paneID)
	return presence == PanePresentAlive, err
}
func (r *TmuxInteractiveRuntime) PanePresence(paneID string) (PanePresence, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = interactiveCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := r.command(ctx, "list-panes", "-t", paneID, "-F", "#{pane_id}\t#{pane_dead}").CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.Contains(strings.ToLower(string(out)), "can't find pane") {
			return PaneAbsent, nil
		}
		return PaneAbsent, fmt.Errorf("probe tmux pane %q: %w: %s", paneID, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != paneID {
			continue
		}
		if fields[1] == "1" {
			return PanePresentDead, nil
		}
		return PanePresentAlive, nil
	}
	return PaneAbsent, nil
}
func (r *TmuxInteractiveRuntime) Kill(paneID string) error {
	err := r.run(context.Background(), "kill-pane", "-t", paneID)
	if err == nil {
		r.mu.Lock()
		delete(r.classifiers, paneID)
		delete(r.placements, paneID)
		r.mu.Unlock()
	}
	return err
}
func (r *TmuxInteractiveRuntime) ReapplyLayout(target string) error {
	if target == "" {
		return nil
	}
	return r.run(context.Background(), "select-layout", "-t", target, "main-horizontal")
}
func (r *TmuxInteractiveRuntime) SessionAlive(session string) (bool, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = interactiveCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := r.command(ctx, "has-session", "-t", session).CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.Contains(strings.ToLower(string(out)), "can't find session") {
		return false, nil
	}
	return false, fmt.Errorf("probe tmux session %q: %w: %s", session, err, strings.TrimSpace(string(out)))
}
func (r *TmuxInteractiveRuntime) ViewerOverrides(ctx context.Context, session string) ViewerOverrides {
	return ReadViewerSessionOverrides(ctx, r.tmuxPath, session, r.ExecCommandContext)
}
func (r *TmuxInteractiveRuntime) FindPaneByDispatchID(windowID, dispatchID string) (string, error) {
	out, err := r.output(context.Background(), "list-panes", "-t", windowID, "-F", "#{pane_id}\t#{pane_start_command}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) == 2 && startCommandHasDispatchID(fields[1], dispatchID) {
			return fields[0], nil
		}
	}
	return "", nil
}

func startCommandHasDispatchID(command, dispatchID string) bool {
	words := shellCommandWords(command)
	for i, word := range words {
		if word == dispatchIDEnv+"="+dispatchID && i > 0 && filepath.Base(words[i-1]) == "env" {
			return true
		}
		if i+2 < len(words) && word == "dispatch" && words[i+1] == "watch" && words[i+2] == dispatchID {
			return true
		}
	}
	return false
}

func shellCommandWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			flush()
			continue
		}
		word.WriteRune(r)
	}
	flush()
	return words
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
