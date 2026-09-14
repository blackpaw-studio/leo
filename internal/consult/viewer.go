package consult

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	dispatchViewerSession = "leo-dispatch"
	viewerCommandTimeout  = 5 * time.Second
	viewerWaitDelay       = 100 * time.Millisecond
	viewerGraceAfterEnd   = time.Hour
)

// Viewer opens an inspectable tmux window for asynchronous dispatches. It is
// deliberately an optional Dispatcher hook: dispatch execution and recording
// remain useful when tmux is unavailable.
type Viewer struct {
	once               sync.Once
	mu                 sync.Mutex
	windowIDs          map[string]string
	rosterMu           sync.Mutex
	rosters            map[string]rosterSessionState
	ConfigPath         string
	TmuxPath           string
	Executable         func() (string, error)
	ExecCommand        func(name string, args ...string) *exec.Cmd
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
	Timeout            time.Duration
	ResolveCaller      func(caller string) (session string, ok bool)
	Logf               func(format string, args ...any)
}

// NewViewer returns a viewer using Leo's dedicated tmux server. resolveCaller
// should return a caller's supervised tmux session only while it is live.
func NewViewer(configPath string, resolveCaller func(string) (string, bool)) *Viewer {
	return &Viewer{
		ConfigPath:    configPath,
		TmuxPath:      "tmux",
		Executable:    os.Executable,
		ExecCommand:   exec.Command,
		ResolveCaller: resolveCaller,
		Logf:          log.Printf,
	}
}

// OnStart is a Dispatcher start hook. It intentionally returns no error: a
// missing tmux binary or a racing session teardown must never reject work.
func (v *Viewer) OnStart(rec Record) string {
	if rec.Kind != "dispatch" {
		return ""
	}
	if v == nil {
		return ""
	}
	v.defaults()
	session := ""
	if v.ResolveCaller != nil && rec.Caller != "" {
		if candidate, ok := v.ResolveCaller(rec.Caller); ok && v.run("has-session", "-t", tmux.Target(candidate)) == nil {
			session = candidate
		}
	}
	if session == "" {
		session = dispatchViewerSession
		if v.run("has-session", "-t", tmux.Target(session)) != nil {
			if err := v.run("new-session", "-d", "-s", session); err != nil {
				// Another simultaneous dispatch may have created it between our
				// probe and new-session. Only give up when it is still absent.
				if v.run("has-session", "-t", tmux.Target(session)) != nil {
					v.log("creating fallback session %q: %v", session, err)
					return ""
				}
			}
		}
	}

	leo, err := v.Executable()
	if err != nil {
		v.log("resolving leo executable: %v", err)
		return ""
	}
	watch := fmt.Sprintf("%s --config %s dispatch watch %s", shellQuote(leo), shellQuote(v.ConfigPath), rec.ID)
	name := viewerWindowName(rec)
	out, err := v.output("new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmux.Target(session), "-n", name, watch)
	if err != nil {
		v.log("opening dispatch viewer %q: %v", rec.ID, err)
		return ""
	}
	windowID := strings.TrimSpace(string(out))
	if windowID == "" {
		v.log("opening dispatch viewer %q: tmux returned no window ID", rec.ID)
		return ""
	}
	v.mu.Lock()
	if v.windowIDs == nil {
		v.windowIDs = make(map[string]string)
	}
	v.windowIDs[rec.ID] = windowID
	v.mu.Unlock()
	if err := v.run("set-window-option", "-t", windowID, "remain-on-exit", "on"); err != nil {
		v.log("keeping dispatch viewer %q open: %v", rec.ID, err)
	}
	return windowID
}

func (v *Viewer) defaults() {
	v.once.Do(func() { v.setDefaults() })
}

func (v *Viewer) setDefaults() {
	if v.TmuxPath == "" {
		if path, err := tmux.Locate(); err == nil {
			v.TmuxPath = path
		} else {
			v.TmuxPath = "tmux"
		}
	}
	if v.Executable == nil {
		v.Executable = os.Executable
	}
	if v.ExecCommand == nil && v.ExecCommandContext == nil {
		v.ExecCommand = exec.Command
	}
	if v.Timeout <= 0 {
		v.Timeout = viewerCommandTimeout
	}
	if v.Logf == nil {
		v.Logf = log.Printf
	}
}

// Close releases a completed dispatch's viewer. Only successful results are
// collected immediately; other terminal states remain available for diagnosis.
func (v *Viewer) Close(rec Record) {
	if v == nil || rec.Kind != "dispatch" || rec.Mode == ModeInteractive || rec.Status != StatusDone {
		return
	}
	v.defaults()
	if rec.ViewerWindowID != "" {
		v.mu.Lock()
		if v.windowIDs == nil {
			v.windowIDs = make(map[string]string)
		}
		if v.windowIDs[rec.ID] == "" {
			v.windowIDs[rec.ID] = rec.ViewerWindowID
		}
		v.mu.Unlock()
	}
	v.kill(rec.ID)
}

// Sweep closes viewers for terminal dispatches after their post-mortem grace
// period. It intentionally only considers tracked windows, never arbitrary
// dead panes in a tmux session.
func (v *Viewer) Sweep(records []Record, now time.Time) {
	if v == nil {
		return
	}
	v.defaults()
	for _, rec := range records {
		if rec.ViewerWindowID != "" {
			v.mu.Lock()
			if v.windowIDs == nil {
				v.windowIDs = make(map[string]string)
			}
			v.windowIDs[rec.ID] = rec.ViewerWindowID
			v.mu.Unlock()
		}
		if rec.Kind != "dispatch" || rec.Mode == ModeInteractive || !rec.Status.Terminal() || rec.EndedAt.IsZero() || now.Before(rec.EndedAt.Add(viewerGraceAfterEnd)) {
			continue
		}
		v.kill(rec.ID)
	}
}

func (v *Viewer) kill(id string) {
	v.mu.Lock()
	windowID := v.windowIDs[id]
	if windowID != "" {
		delete(v.windowIDs, id)
	}
	v.mu.Unlock()
	if windowID == "" {
		return
	}
	if err := v.run("kill-window", "-t", windowID); err != nil {
		v.log("closing dispatch viewer %q: %v", id, err)
	}
}

func (v *Viewer) command(ctx context.Context, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if v.ExecCommandContext != nil {
		cmd = v.ExecCommandContext(ctx, v.TmuxPath, tmux.Args(args...)...)
	} else {
		cmd = v.ExecCommand(v.TmuxPath, tmux.Args(args...)...)
	}
	// After the deadline kills tmux, a forked grandchild (dash forks where
	// bash execs) can keep the output pipe open; WaitDelay stops Wait from
	// blocking on that pipe until the orphan exits.
	cmd.WaitDelay = viewerWaitDelay
	return cmd
}

func (v *Viewer) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), v.Timeout)
	defer cancel()
	return v.command(ctx, args...).Run()
}

func (v *Viewer) output(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), v.Timeout)
	defer cancel()
	return v.command(ctx, args...).Output()
}
func (v *Viewer) log(format string, args ...any) { v.Logf("dispatch viewer: "+format, args...) }

func viewerWindowName(rec Record) string {
	label := rec.Name
	if label == "" {
		label = rec.Template
	}
	label = sanitizeViewerLabel(label)
	chars := []rune(label)
	if len(chars) > 24 {
		label = string(chars[:24])
	}
	if label == "" {
		label = "dispatch"
	}
	hex := strings.TrimPrefix(rec.ID, "d-")
	if len(hex) > 4 {
		hex = hex[len(hex)-4:]
	}
	return label + "·" + hex
}

func sanitizeViewerLabel(label string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == ':' || r == '.' {
			return '-'
		}
		return r
	}, label)
}

// shellQuote returns one POSIX-shell word. tmux executes new-window's command
// through a shell, so an executable path must not be interpolated raw.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
