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

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	dispatchViewerSession = "leo-dispatch"
	viewerCommandTimeout  = 5 * time.Second
	viewerWaitDelay       = 100 * time.Millisecond
	viewerGraceAfterEnd   = time.Hour
	viewerHandledLimit    = 1024
)

// Viewer opens an inspectable tmux window for asynchronous dispatches. It is
// deliberately an optional Dispatcher hook: dispatch execution and recording
// remain useful when tmux is unavailable.
type Viewer struct {
	once               sync.Once
	mu                 sync.Mutex
	windowIDs          map[string]string
	Coordinator        *ViewerPlacementCoordinator
	handledWindowIDs   map[string]string
	handledWindowOrder []handledWindow
	rosterMu           sync.Mutex
	rosters            map[string]rosterSessionState
	rosterInventoryLog string
	rosterZeroLogged   bool
	rosterEvents       map[string]bool
	ConfigPath         string
	TmuxPath           string
	Executable         func() (string, error)
	ExecCommand        func(name string, args ...string) *exec.Cmd
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
	Timeout            time.Duration
	ResolveCaller      func(caller string) (session string, ok bool)
	Logf               func(format string, args ...any)
	Records            func() []Record
	PersistRecord      func(Record)
	PersistIntent      func(Record)
	ClosePane          func(Record, string, func(string) error, func(string) error) (Record, error)
}

type handledWindow struct {
	recordID string
	windowID string
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
	if rec.CallerPaneID != "" && rec.CallerSessionID != "" && rec.CallerSessionID != dispatchViewerSession {
		cfg, err := config.Load(v.ConfigPath)
		if err == nil {
			overrides := ReadViewerSessionOverrides(context.Background(), v.TmuxPath, rec.CallerSessionID, func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if v.ExecCommandContext != nil {
					return v.ExecCommandContext(ctx, name, args...)
				}
				return v.ExecCommand(name, args...)
			})
			if v.Coordinator == nil {
				v.Coordinator = NewViewerPlacementCoordinator()
			}
			placement := v.Coordinator.Decide(rec, overrides, cfg, v.Records)
			if placement.Kind == "split" {
				rec.ViewerKind = "split"
				rec.ViewerTitle = viewerWindowName(rec)
				if v.PersistIntent != nil {
					v.PersistIntent(rec)
				}
				if pane := v.openSplit(rec, placement); pane != "" {
					return pane
				}
				v.Coordinator.Cancel(rec.ID)
			}
		}
	}
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
	// A window cannot have remain-on-exit pre-set before it exists (unlike
	// the caller's window in openSplit), so create it with a placeholder
	// command that cannot exit, turn the option on, then respawn the real
	// watch command into the same pane. This avoids the same race as
	// split-window: a fast-exiting watch process closing the window
	// before a later set-window-option ever runs. respawn-pane exists on
	// every tmux >= 3.2, our minimum supported version.
	out, err := v.output("new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmux.Target(session), "-n", name, "sleep 86400")
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
		v.abandonWindow(rec.ID, windowID)
		return ""
	}
	if err := v.run("respawn-pane", "-k", "-t", windowID, watch); err != nil {
		v.log("starting dispatch viewer %q: %v", rec.ID, err)
		v.abandonWindow(rec.ID, windowID)
		return ""
	}
	return windowID
}

// abandonWindow kills a placeholder window that failed to become a real
// viewer (remain-on-exit never applied, or the watch command never
// started), so it doesn't linger looking like a live viewer.
func (v *Viewer) abandonWindow(recID, windowID string) {
	v.mu.Lock()
	delete(v.windowIDs, recID)
	v.mu.Unlock()
	if err := v.run("kill-window", "-t", windowID); err != nil {
		v.log("killing abandoned dispatch viewer window %q: %v", windowID, err)
	}
}

func (v *Viewer) openSplit(rec Record, placement ViewerPlacement) string {
	leo, err := v.Executable()
	if err != nil {
		return ""
	}
	watch := fmt.Sprintf("%s --config %s dispatch watch %s", shellQuote(leo), shellQuote(v.ConfigPath), rec.ID)
	// remain-on-exit must be on before the pane is created: a fast-exiting
	// watch process can close the pane before a later, pane-scoped
	// set-option ever runs (a real race, observed on Linux tmux where
	// process startup is quick relative to macOS). It is set at window
	// scope only long enough to cover that gap, then pinned onto the new
	// pane specifically and unset from the window again immediately after
	// — leaving it at window scope would make every other pane in the
	// caller's window (including the caller's own) linger dead on exit,
	// which the agent supervisor does not expect.
	_ = v.run("set-window-option", "-t", rec.CallerWindowID, "remain-on-exit", "on")
	out, err := v.output("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", placement.Target, "-c", rec.Cwd, watch)
	if err != nil {
		_ = v.run("set-window-option", "-u", "-t", rec.CallerWindowID, "remain-on-exit")
		return ""
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		_ = v.run("set-window-option", "-u", "-t", rec.CallerWindowID, "remain-on-exit")
		return ""
	}
	_ = v.run("select-pane", "-t", pane, "-T", viewerWindowName(rec))
	_ = v.run("set-option", "-p", "-t", pane, "remain-on-exit", "on")
	_ = v.run("set-window-option", "-u", "-t", rec.CallerWindowID, "remain-on-exit")
	_ = v.run("set-option", "-w", "-t", rec.CallerWindowID, "main-pane-height", fmt.Sprintf("%d%%", placement.MainPaneHeight))
	_ = v.run("select-layout", "-t", rec.CallerWindowID, "main-horizontal")
	return pane
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
	if v.ClosePane == nil {
		v.ClosePane = NewDispatcher(nil).CloseRecordedPane
	}
}

// Close releases a completed dispatch's viewer. Only successful results are
// collected immediately; other terminal states remain available for diagnosis.
func (v *Viewer) Close(rec Record) {
	if v == nil || rec.Kind != "dispatch" || rec.Mode == ModeInteractive || rec.Status != StatusDone {
		return
	}
	v.defaults()
	if v.Coordinator != nil {
		v.Coordinator.Cancel(rec.ID)
	}
	if rec.ViewerKind == "split" && rec.ViewerPaneID != "" {
		if v.ClosePane != nil {
			_, _ = v.ClosePane(rec, rec.ViewerPaneID, func(p string) error { return v.run("kill-pane", "-t", p) }, func(w string) error { return v.run("select-layout", "-t", w, "main-horizontal") })
		}
		return
	}
	if rec.ViewerWindowID != "" {
		v.mu.Lock()
		if v.windowIDs == nil {
			v.windowIDs = make(map[string]string)
		}
		if v.handledWindowIDs[rec.ID] != rec.ViewerWindowID && v.windowIDs[rec.ID] == "" {
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
	v.pruneHandledWindows(records)
	for _, rec := range records {
		expired := rec.Kind == "dispatch" && rec.Mode != ModeInteractive && rec.Status.Terminal() && !rec.EndedAt.IsZero() && !now.Before(rec.EndedAt.Add(viewerGraceAfterEnd))
		if expired {
			if rec.ViewerKind == "split" && rec.ViewerPaneID != "" {
				if v.ClosePane != nil {
					_, _ = v.ClosePane(rec, rec.ViewerPaneID, func(p string) error { return v.run("kill-pane", "-t", p) }, func(w string) error { return v.run("select-layout", "-t", w, "main-horizontal") })
				}
				continue
			}
			_ = v.killWindow(rec.ID, rec.ViewerWindowID)
			continue
		}
		if rec.ViewerWindowID != "" && !rec.Status.Terminal() {
			v.mu.Lock()
			if v.windowIDs == nil {
				v.windowIDs = make(map[string]string)
			}
			if v.handledWindowIDs[rec.ID] != rec.ViewerWindowID {
				v.windowIDs[rec.ID] = rec.ViewerWindowID
			}
			v.mu.Unlock()
		}
	}
}

func (v *Viewer) kill(id string) {
	_ = v.killWindow(id, "")
}

func (v *Viewer) killWindow(id, persistedWindowID string) error {
	return v.killWindowWithRetry(id, persistedWindowID, false)
}

func (v *Viewer) killWindowWithRetry(id, persistedWindowID string, retry bool) error {
	v.mu.Lock()
	if persistedWindowID != "" && v.handledWindowIDs[id] == persistedWindowID {
		v.mu.Unlock()
		return nil
	}
	windowID := v.windowIDs[id]
	if windowID == "" {
		windowID = persistedWindowID
	}
	v.mu.Unlock()
	if windowID == "" {
		return nil
	}
	if err := v.run("kill-window", "-t", windowID); err != nil {
		v.log("closing dispatch viewer %q: %v", id, err)
		if retry {
			v.mu.Lock()
			if v.handledWindowIDs == nil {
				v.handledWindowIDs = make(map[string]string)
			}
			if v.windowIDs == nil {
				v.windowIDs = make(map[string]string)
			}
			delete(v.handledWindowIDs, id)
			v.windowIDs[id] = windowID
			v.mu.Unlock()
		}
		return err
	}
	v.mu.Lock()
	delete(v.windowIDs, id)
	v.recordHandledWindow(id, windowID)
	v.mu.Unlock()
	return nil
}

// CloseFinished closes a terminal headless viewer regardless of outcome.
func (v *Viewer) CloseFinished(rec Record, layout func(string) error) (Record, error) {
	v.defaults()
	if rec.ViewerKind == "split" && rec.ViewerPaneID != "" {
		return v.ClosePane(rec, rec.ViewerPaneID, func(p string) error { return v.run("kill-pane", "-t", p) }, layout)
	}
	if rec.ViewerWindowID != "" {
		if err := v.killWindowWithRetry(rec.ID, rec.ViewerWindowID, true); err != nil {
			return rec, err
		}
		rec.ViewerWindowID = ""
		if v.PersistRecord != nil {
			v.PersistRecord(rec)
		}
	}
	return rec, nil
}

// recordHandledWindow retains only the most recently handled persisted window
// identities. v.mu must be held by the caller.
func (v *Viewer) recordHandledWindow(recordID, windowID string) {
	if v.handledWindowIDs == nil {
		v.handledWindowIDs = make(map[string]string)
	}
	if v.handledWindowIDs[recordID] == windowID {
		return
	}
	if _, found := v.handledWindowIDs[recordID]; found {
		for i, handled := range v.handledWindowOrder {
			if handled.recordID == recordID {
				v.handledWindowOrder = append(v.handledWindowOrder[:i], v.handledWindowOrder[i+1:]...)
				break
			}
		}
	}
	v.handledWindowIDs[recordID] = windowID
	v.handledWindowOrder = append(v.handledWindowOrder, handledWindow{recordID: recordID, windowID: windowID})
	for len(v.handledWindowOrder) > viewerHandledLimit {
		oldest := v.handledWindowOrder[0]
		v.handledWindowOrder = v.handledWindowOrder[1:]
		if v.handledWindowIDs[oldest.recordID] == oldest.windowID {
			delete(v.handledWindowIDs, oldest.recordID)
		}
	}
}

func (v *Viewer) pruneHandledWindows(records []Record) {
	present := make(map[string]struct{}, len(records))
	for _, rec := range records {
		present[rec.ID] = struct{}{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for recordID := range v.handledWindowIDs {
		if _, found := present[recordID]; !found {
			delete(v.handledWindowIDs, recordID)
		}
	}
	if len(v.handledWindowOrder) == 0 {
		return
	}
	retained := v.handledWindowOrder[:0]
	for _, handled := range v.handledWindowOrder {
		if v.handledWindowIDs[handled.recordID] == handled.windowID {
			retained = append(retained, handled)
		}
	}
	v.handledWindowOrder = retained
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
