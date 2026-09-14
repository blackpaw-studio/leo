package consult

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	dispatchViewerSession = "leo-dispatch"
	viewerCommandTimeout  = 5 * time.Second
	viewerWaitDelay       = 100 * time.Millisecond
)

var dispatchID = regexp.MustCompile(`^d-[0-9a-f]+$`)

// Viewer opens an inspectable tmux window for asynchronous dispatches. It is
// deliberately an optional Dispatcher hook: dispatch execution and recording
// remain useful when tmux is unavailable.
type Viewer struct {
	once               sync.Once
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
func (v *Viewer) OnStart(rec Record) {
	if rec.Kind != "dispatch" {
		return
	}
	if v == nil {
		return
	}
	v.defaults()
	v.prune()

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
					return
				}
			}
		}
	}

	leo, err := v.Executable()
	if err != nil {
		v.log("resolving leo executable: %v", err)
		return
	}
	watch := fmt.Sprintf("%s --config %s dispatch watch %s", shellQuote(leo), shellQuote(v.ConfigPath), rec.ID)
	if err := v.run("new-window", "-d", "-t", tmux.Target(session), "-n", rec.ID, watch); err != nil {
		v.log("opening dispatch viewer %q: %v", rec.ID, err)
		return
	}
	if err := v.run("set-window-option", "-t", windowTarget(session, rec.ID), "remain-on-exit", "on"); err != nil {
		v.log("keeping dispatch viewer %q open: %v", rec.ID, err)
	}
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

func (v *Viewer) prune() {
	out, err := v.output("list-panes", "-a", "-F", "#{pane_dead}\t#{window_name}\t#{window_id}")
	if err != nil {
		v.log("listing stale dispatch viewers: %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] != "1" || !dispatchID.MatchString(fields[1]) {
			continue
		}
		if err := v.run("kill-window", "-t", fields[2]); err != nil {
			v.log("pruning dispatch viewer %q: %v", fields[1], err)
		}
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

func windowTarget(session, name string) string { return tmux.Target(session) + ":=" + name }

// shellQuote returns one POSIX-shell word. tmux executes new-window's command
// through a shell, so an executable path must not be interpolated raw.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
