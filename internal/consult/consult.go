// Package consult runs one-off headless "second opinion" subagents and
// returns their final text synchronously to the caller.
package consult

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// dispatchIDEnv names the variable every dispatched or consulted run
// carries, set to its dispatch id.
const dispatchIDEnv = "LEO_DISPATCH_ID"

const (
	// RunTimeout is the authoritative deadline for one consult. A consult is
	// a full agent run, so this is generous; it stays a hard cap only so a
	// wedged harness process can't hold one of maxConcurrent slots forever.
	// Harness-side MCP tool ceilings are derived from it (leomcp.ToolTimeout)
	// so leo, not the coding agent, is what times a consult out.
	RunTimeout    = 30 * time.Minute
	maxConcurrent = 6
)

// ValidationError reports a request/configuration problem that should be
// returned to API clients as a 4xx response rather than an execution failure.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func invalidf(format string, args ...any) error {
	return &ValidationError{Err: fmt.Errorf(format, args...)}
}

type Dispatcher struct {
	sem                  chan struct{}
	recorder             Recorder
	ExecCommandContext   func(ctx context.Context, name string, args ...string) *exec.Cmd
	ProcessCommand       func(ctx context.Context, name string, args ...string) *exec.Cmd
	GitCommand           func(name string, args ...string) *exec.Cmd
	WorktreeSuffix       func() string
	daemonCtx            context.Context
	mu                   sync.Mutex
	runs                 map[string]*runState
	onStart              func(Record) string
	onCollect            func(Record)
	interactiveRuntime   InteractiveRuntime
	now                  func() time.Time
	waits                map[string]int
	serial               map[string]*serialLock
	placement            *ViewerPlacementCoordinator
	waitResolvedHook     func()
	waitDoneHook         func(string)
	beforeOpeningInject  func()
	afterOpeningInject   func()
	beforeSendInjectable func()
	notificationDelivery NotificationDelivery
	closeFinishedViewer  func(Record, func(string) error) (Record, error)
}

func (d *Dispatcher) SetCloseFinishedViewer(fn func(Record, func(string) error) (Record, error)) {
	d.mu.Lock()
	d.closeFinishedViewer = fn
	d.mu.Unlock()
}

type runState struct {
	record         Record
	handle         Handle
	done           chan struct{}
	cancel         context.CancelFunc
	settleDeadline time.Time
	settleStatus   Status
	lastHook       time.Time
	armedTurn      string
	armedUntil     time.Time
	pendingCloses  map[string]pendingClose
	eventIDs       []string
	closedHarness  map[string]bool
	closedIDs      []string
	idleSince      time.Time
	killPending    bool
	releasing      bool
	// pgid is the headless command's private process group. It is retained
	// after Wait so worktree cleanup can prove no detached child remains.
	pgid            int
	headlessStarted bool
}

// NewDispatcher builds a dispatcher recording through rec. A nil recorder
// discards recordings, leaving behavior exactly as it was before consults
// were observable.
// NewDispatcher builds a dispatcher. parent controls the lifetime of accepted
// runs; nil retains the historical background-context behavior for callers
// that do not own a service lifetime.
func NewDispatcher(rec Recorder, parent ...context.Context) *Dispatcher {
	var ctx context.Context
	if len(parent) > 0 {
		ctx = parent[0]
	}
	return NewDispatcherWithOnStart(rec, ctx, nil)
}

// NewDispatcherWithOnStart builds a dispatcher with an optional best-effort
// start hook. Hooks observe accepted runs only and cannot reject a dispatch.
func NewDispatcherWithOnStart(rec Recorder, parent context.Context, onStart func(Record) string, onCollect ...func(Record)) *Dispatcher {
	if rec == nil {
		rec = nopRecorder{}
	}
	daemonCtx := context.Background()
	if parent != nil {
		daemonCtx = parent
	}
	d := &Dispatcher{
		sem:                make(chan struct{}, maxConcurrent),
		recorder:           rec,
		ExecCommandContext: exec.CommandContext,
		ProcessCommand:     exec.CommandContext,
		GitCommand:         exec.Command,
		WorktreeSuffix:     worktreeSuffix,
		daemonCtx:          daemonCtx,
		runs:               make(map[string]*runState),
		onStart:            onStart,
		now:                time.Now,
		waits:              make(map[string]int),
		serial:             make(map[string]*serialLock),
		placement:          NewViewerPlacementCoordinator(),
	}
	if len(onCollect) > 0 {
		d.onCollect = onCollect[0]
	}
	return d
}

// templateNames returns the configured template names, sorted, for use in
// error messages. Returns "(none configured)" when the config has no
// templates so the message never trails off into nothing.
func templateNames(cfg *config.Config) string {
	if len(cfg.Templates) == 0 {
		return "(none configured)"
	}
	names := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// newID mints a consult id short enough to read in a table and wide enough
// that collisions across the handful of retained records are not a concern.
// Callers abbreviate it to a unique prefix anyway.
func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "d-" + hex.EncodeToString(b[:])
}

// Start validates a dispatch and begins it under the dispatcher context.
// Request contexts govern only validation and the immediate caller, never the
// lifetime of an accepted run.
func (d *Dispatcher) Start(_ context.Context, cfg *config.Config, req Request) (Started, error) {
	if req.Isolation != "" && req.Isolation != "worktree" {
		return Started{}, invalidf("isolation must be empty or \"worktree\"")
	}
	mode := req.Mode
	if mode == "" {
		mode = ModeHeadless
	}
	if mode != ModeHeadless && mode != ModeInteractive {
		return Started{}, invalidf("unknown dispatch mode %q", req.Mode)
	}
	if req.Timeout < 0 {
		return Started{}, invalidf("timeout must be non-negative")
	}
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		// List the valid names so a caller that guessed a model name instead
		// of a template ("opus") can retry without a second tool call.
		return Started{}, invalidf("unknown template %q; available templates: %s", req.Template, templateNames(cfg))
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return Started{}, invalidf("resolving harness for template %q: %v", req.Template, err)
	}
	if !h.SupportsKind(harness.KindTask) {
		return Started{}, invalidf("harness %q does not support one-shot runs", h.Name())
	}
	if mode == ModeInteractive {
		if h.Name() == "opencode" {
			return Started{}, invalidf("interactive dispatch is not supported by harness %q", h.Name())
		}
		d.mu.Lock()
		rt := d.interactiveRuntime
		d.mu.Unlock()
		if rt == nil {
			return Started{}, invalidf("interactive runtime is not configured")
		}
	}
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Started{}, invalidf("model for consult: %v", err)
	}
	if req.Effort != "" {
		validator, ok := h.(harness.EffortValidator)
		if !ok {
			return Started{}, invalidf("effort not supported by harness %s", h.Name())
		}
		if err := validator.ValidateEffort(req.Effort); err != nil {
			return Started{}, invalidf("effort for dispatch: %v", err)
		}
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Started{}, invalidf("template %q harness_options: %v", req.Template, err)
	}
	if opts, ok := decoded.(claudeharness.Options); ok {
		decoded = resolveClaudeDispatchProfile(cfg, tmpl, requestKind(req), opts, tmpl.Env)
	}
	if req.Cwd == "" || !filepath.IsAbs(req.Cwd) {
		return Started{}, invalidf("cwd must be an existing absolute directory")
	}
	info, err := os.Stat(req.Cwd)
	if err != nil || !info.IsDir() {
		return Started{}, invalidf("cwd must be an existing absolute directory")
	}

	// Record before competing for a slot, so consults waiting behind the
	// concurrency limit are visible too. Validation failures never ran and
	// are deliberately not recorded.
	kind := requestKind(req)
	timeout := req.Timeout
	if kind == "consult" && timeout == 0 {
		timeout = RunTimeout
	}
	notify := requestKind(req) == "dispatch"
	if notify && req.Notify != nil {
		notify = *req.Notify
	}
	rec := Record{
		ID: newID(), Caller: req.Caller, Template: req.Template, Role: req.Role, Profile: req.Profile,
		Kind: kind, Harness: h.Name(), Model: model, Cwd: req.Cwd, Name: req.Name, Timeout: timeout,
		Effort: req.Effort,
		Prompt: req.Prompt, Status: StatusQueued, StartedAt: d.now(), Mode: mode,
		Notify: notify, Isolation: req.Isolation, SourceCwd: req.Cwd,
		CallerPaneID: req.CallerPaneID, CallerHarness: req.CallerHarness, CallerSessionID: req.CallerSessionID, CallerWindowID: req.CallerWindowID,
	}
	handle, err := d.recorder.Open(rec)
	if err != nil {
		if req.Isolation == "worktree" {
			return Started{}, fmt.Errorf("recording isolated dispatch metadata: %w", err)
		}
		// Recording is best-effort. An unwritable state directory should
		// cost visibility, not the answer the caller is waiting for.
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", rec.ID, err)
		handle = nopHandle{}
	}
	runCtx, cancel := context.WithCancel(d.daemonCtx)
	state := &runState{record: rec, handle: handle, done: make(chan struct{}), cancel: cancel}
	if mode == ModeHeadless {
		state.record.Turns = append(state.record.Turns, Turn{TurnID: rec.ID + "#1", Source: TurnSourceOrchestrator, StartedAt: rec.StartedAt, Delivered: true, Text: req.Prompt})
		d.beginUsageInvocationLocked(state)
		if _, ok := state.handle.(interactiveRecordHandle); ok {
			d.persistLocked(state, "turn")
		} else {
			d.persistRecordLocked(state)
		}
	}
	d.mu.Lock()
	d.runs[rec.ID] = state
	d.pruneTerminalRunsLocked()
	d.mu.Unlock()
	if req.Isolation == "worktree" {
		if err := d.prepareWorktree(state, req); err != nil {
			d.complete(state, StatusFailed, "", err)
			close(state.done)
			return Started{}, err
		}
		req.Cwd = state.record.Worktree
		rec = cloneRecord(state.record)
	}
	spec := harness.LaunchSpec{
		Kind: harness.KindTask, Name: req.Name, Model: model, Effort: req.Effort,
		MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Cwd,
		Prompt: requestPrompt(req), Options: decoded, Dispatched: true,
	}
	if spec.Name == "" {
		spec.Name = "dispatch"
	}
	args, err := h.Args(spec)
	if err != nil {
		d.complete(state, StatusFailed, "", err)
		close(state.done)
		d.cleanupWorktree(rec.ID)
		return Started{}, invalidf("building %s args: %v", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		d.complete(state, StatusFailed, "", err)
		close(state.done)
		d.cleanupWorktree(rec.ID)
		return Started{}, invalidf("building %s env: %v", h.Name(), err)
	}
	if mode == ModeInteractive {
		started, startErr := d.startInteractive(runCtx, state, req, h.Name(), model, cfg)
		if startErr != nil && req.Isolation == "worktree" {
			d.cleanupWorktree(rec.ID)
		}
		return started, startErr
	}
	if rec.Kind == "dispatch" && d.onStart != nil {
		if windowID := d.onStart(rec); windowID != "" {
			d.placement.Publish(rec.ID, func() bool {
				d.mu.Lock()
				if strings.HasPrefix(windowID, "%") {
					state.record.ViewerKind, state.record.ViewerPaneID = "split", windowID
				} else {
					state.record.ViewerKind, state.record.ViewerWindowID, state.record.ViewerTitle = "window", windowID, ""
				}
				rec = state.record
				d.mu.Unlock()
				if rh, ok := handle.(recordHandle); ok {
					_ = rh.SetRecord(rec)
				} else if err := handle.SetViewerWindowID(windowID); err != nil {
					fmt.Fprintf(os.Stderr, "dispatch %s: recording viewer: %v\n", rec.ID, err)
				}
				return true
			})
		} else {
			d.placement.Cancel(rec.ID)
		}
	}
	go d.runInvocation(runCtx, state, state.done, h, model, tmpl.Env, args, harnessEnv, req.Cwd, timeout, false)
	return Started{ID: rec.ID, Harness: h.Name(), Model: model, Cwd: req.Cwd}, nil
}

func (d *Dispatcher) run(parent context.Context, state *runState, h harness.Harness, model string, env map[string]string, args []string, harnessEnv map[string]string, cwd string, timeout time.Duration) {
	d.runInvocation(parent, state, state.done, h, model, env, args, harnessEnv, cwd, timeout, false)
}

func (d *Dispatcher) runInvocation(parent context.Context, state *runState, done chan struct{}, h harness.Harness, model string, env map[string]string, args []string, harnessEnv map[string]string, cwd string, timeout time.Duration, slotHeld bool) {
	defer close(done)
	if slotHeld {
		defer func() { <-d.sem }()
	} else {
		select {
		case d.sem <- struct{}{}:
			defer func() { <-d.sem }()
		case <-parent.Done():
			d.complete(state, StatusCanceled, "", parent.Err())
			return
		}
	}
	// A queued run can be canceled at the same instant a concurrency slot
	// opens. Do not publish a misleading running transition in that race.
	if err := parent.Err(); err != nil {
		d.complete(state, StatusCanceled, "", err)
		return
	}
	runCtx := parent
	timeoutCancel := func() {}
	if timeout > 0 {
		runCtx, timeoutCancel = context.WithTimeout(parent, timeout)
	}
	defer timeoutCancel()
	binary := h.Binary()
	if p, err := exec.LookPath(binary); err == nil {
		binary = p
	}
	cmd := d.ExecCommandContext(runCtx, binary, args...)
	cmd.Dir = cwd
	d.mu.Lock()
	dispatchID := state.record.ID
	d.mu.Unlock()
	// LEO_DISPATCH_ID is applied last so neither an identity the daemon
	// inherited nor a template env can mask it: the run's own tools (leo MCP,
	// `leo dispatch report`) rely on it to know they are inside a dispatch.
	cmd.Env = mergedEnv(os.Environ(), harnessEnv, env, map[string]string{dispatchIDEnv: dispatchID})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second

	// The tee is assigned to both Stdout and Stderr as the *same* Writer
	// value: os/exec only serializes concurrent writes to a shared output
	// when the two are the same value, so this keeps the harness's combined
	// output in order — the behavior CombinedOutput used to supply.
	tee := &recordingTee{handle: state.handle}
	if factory, ok := h.(harness.UsageAccounter); ok {
		tee.usage = factory.NewUsageAccumulator()
		tee.onUsage = func(usage *harness.Usage) {
			d.mu.Lock()
			d.applyUsageLocked(state, usage, false)
			d.mu.Unlock()
		}
	}
	cmd.Stdout, cmd.Stderr = tee, tee

	runErr := cmd.Start()
	if runErr == nil {
		d.mu.Lock()
		if state.record.Isolation == "worktree" {
			state.headlessStarted = true
			if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
				state.pgid = pgid
			}
		}
		if !state.record.Status.Terminal() {
			state.record.Status = StatusRunning
			state.record.startActive(d.now())
			d.persistRecordLocked(state)
		}
		d.mu.Unlock()
		runErr = cmd.Wait()
	}
	parsed, parseErr := h.ParseEvents(bytes.NewReader(tee.Bytes()))
	if parsed.Usage != nil {
		d.mu.Lock()
		d.applyUsageLocked(state, parsed.Usage, true)
		d.mu.Unlock()
	}
	if parsed.SessionID != "" {
		d.mu.Lock()
		state.record.SessionID = parsed.SessionID
		d.persistRecordLocked(state)
		d.mu.Unlock()
	}
	if runCtx.Err() != nil {
		status := StatusTimeout
		if errors.Is(runCtx.Err(), context.Canceled) {
			status = StatusCanceled
		}
		d.complete(state, status, "", fmt.Errorf("consult %s/%s: %w", h.Name(), model, runCtx.Err()))
		return
	}
	if runErr != nil {
		detail := runErr.Error()
		if len(parsed.Errors) > 0 {
			detail += ": " + parsed.Errors[0]
		}
		d.complete(state, StatusFailed, "", fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail))
		return
	}
	if parseErr != nil {
		d.complete(state, StatusFailed, "", fmt.Errorf("consult %s/%s returned unreadable output: %w", h.Name(), model, parseErr))
		return
	}
	if parsed.IsError {
		detail := "consultant reported an error"
		if len(parsed.Errors) > 0 {
			detail = strings.Join(parsed.Errors, "; ")
		}
		d.complete(state, StatusFailed, "", fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail))
		return
	}
	if parsed.Text == "" {
		d.complete(state, StatusFailed, "", fmt.Errorf("consult %s/%s produced no output", h.Name(), model))
		return
	}
	d.complete(state, StatusDone, parsed.Text, nil)
}

// pruneTerminalRunsLocked bounds completed in-memory runs. Records on disk
// remain available through lookup, while active runs are never evicted.
func (d *Dispatcher) pruneTerminalRunsLocked() {
	terminal := make([]*runState, 0, len(d.runs))
	for _, state := range d.runs {
		worktreeResolved := state.record.Isolation != "worktree" || state.record.WorktreeState == WorktreeRemoved
		if state.record.Status.Terminal() && worktreeResolved && !recordHasUnresolvedNotification(state.record) {
			terminal = append(terminal, state)
		}
	}
	if len(terminal) <= RecordsKept {
		return
	}
	sort.Slice(terminal, func(i, j int) bool {
		return terminal[i].record.EndedAt.Before(terminal[j].record.EndedAt)
	})
	for _, state := range terminal[:len(terminal)-RecordsKept] {
		delete(d.runs, state.record.ID)
	}
}

// Wait returns one entry per requested dispatch once all are terminal or the
// supplied timeout expires. It never polls records.
func (d *Dispatcher) Wait(ctx context.Context, ids []string, timeout time.Duration) (entries []Entry) {
	states := make([]*runState, len(ids))
	dones := make([]chan struct{}, len(ids))
	skipCleanup := make([]bool, len(ids))
	defer func() {
		for i := range entries {
			entries[i] = limitWaitEntry(entries[i])
		}
		unlockSerial := d.serialLocks(ids)
		defer unlockSerial()
		for i := range entries {
			if !entries[i].Status.Terminal() {
				continue
			}
			if skipCleanup[i] {
				continue
			}
			rec, state, err := d.lookup(ids[i])
			if err != nil {
				continue
			}
			if rec.Isolation == "worktree" && states[i] != nil && dones[i] != nil {
				<-dones[i]
				rec, state, err = d.lookup(ids[i])
				if err != nil {
					continue
				}
			}
			// A wait for an older terminal turn must not clean up or collect
			// while a newer invocation owns the run.
			if state != nil && state.done != dones[i] && !rec.Status.Terminal() {
				continue
			}
			rec = d.cleanupWorktree(rec.ID)
			entries[i].Worktree, entries[i].Branch = "", ""
			if rec.WorktreeState == WorktreeKept {
				entries[i].Worktree, entries[i].Branch = rec.Worktree, rec.Branch
			}
			d.collect(rec)
		}
	}()
	entries = make([]Entry, len(ids))
	turnIDs := make([]string, len(ids))
	keys := make([]string, len(ids))
	d.mu.Lock()
	for i, id := range ids {
		entries[i].ID = id
		runID := strings.SplitN(id, "#", 2)[0]
		state := d.runs[runID]
		if state == nil {
			entries[i].Status = StatusUnknown
			entries[i].Err = fmt.Sprintf("unknown dispatch %s", id)
			continue
		}
		rec := cloneRecord(state.record)
		states[i] = state
		dones[i] = state.done
		if rec.Mode == ModeInteractive {
			turnID := id
			if !strings.Contains(id, "#") {
				for j := len(rec.Turns) - 1; j >= 0; j-- {
					if rec.Turns[j].Source == TurnSourceOrchestrator {
						turnID = rec.Turns[j].TurnID
						break
					}
				}
			}
			turnIDs[i] = turnID
			entries[i] = interactiveEntry(rec, turnID, d.now())
		} else {
			turnID := id
			if !strings.Contains(id, "#") && len(rec.Turns) > 0 {
				turnID = rec.Turns[len(rec.Turns)-1].TurnID
			}
			turnIDs[i] = turnID
			entries[i] = headlessEntry(rec, turnID, d.now())
			if strings.Contains(id, "#") && len(rec.Turns) > 0 && turnID != rec.Turns[len(rec.Turns)-1].TurnID && entries[i].Status.Terminal() {
				skipCleanup[i] = true
			}
		}
		keys[i] = transitionKey(id, rec.Mode, turnIDs[i])
	}
	if d.waitResolvedHook != nil {
		d.waitResolvedHook()
	}
	unregister := d.registerWaitsLocked(keys)
	d.mu.Unlock()
	defer unregister()
	for i, state := range states {
		if state != nil {
			continue
		}
		if rec, _, err := d.lookup(ids[i]); err == nil {
			if rec.Mode == ModeInteractive {
				turnID := ids[i]
				if !strings.Contains(turnID, "#") {
					for j := len(rec.Turns) - 1; j >= 0; j-- {
						if rec.Turns[j].Source == TurnSourceOrchestrator {
							turnID = rec.Turns[j].TurnID
							break
						}
					}
				}
				entries[i] = interactiveEntry(rec, turnID, d.now())
			} else {
				turnID := ids[i]
				if !strings.Contains(turnID, "#") && len(rec.Turns) > 0 {
					turnID = rec.Turns[len(rec.Turns)-1].TurnID
				}
				entries[i] = headlessEntry(rec, turnID, d.now())
			}
		}
	}
	deadline := time.NewTimer(timeout)
	if timeout <= 0 {
		deadline.Stop()
	}
	defer deadline.Stop()
	for {
		pending := false
		interactivePending := false
		cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}}
		if timeout > 0 {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(deadline.C)})
		}
		for i, state := range states {
			if state != nil && !entries[i].Status.Terminal() && entries[i].Outcome == "" {
				pending = true
				if d.stateRecord(state).Mode == ModeInteractive {
					interactivePending = true
				}
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(dones[i])})
			}
		}
		if !pending {
			return entries
		}
		// Interactive turns complete before their parent session does. Polling
		// here is deliberate: hooks may arrive from an external process and
		// must wake a wait for a specific turn without making the dispatcher
		// own a goroutine per waiter.
		if interactivePending {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(time.After(25 * time.Millisecond))})
		}
		chosen, _, _ := reflect.Select(cases)
		if chosen == 0 || (timeout > 0 && chosen == 1) {
			for i, state := range states {
				if state != nil && !entries[i].Status.Terminal() && entries[i].Outcome == "" {
					rec := d.stateRecord(state)
					if rec.Mode == ModeInteractive {
						entries[i] = interactiveEntry(rec, turnIDs[i], d.now())
					} else {
						entries[i] = headlessEntry(rec, turnIDs[i], d.now())
					}
					if rec.Mode != ModeInteractive && !entries[i].Status.Terminal() {
						entries[i].Status = StatusRunning
						entries[i].Err = ""
					}
				}
			}
			return entries
		}
		for i, state := range states {
			if state != nil {
				rec := d.stateRecord(state)
				if rec.Mode == ModeInteractive {
					entries[i] = interactiveEntry(rec, turnIDs[i], d.now())
				} else {
					entries[i] = headlessEntry(rec, turnIDs[i], d.now())
				}
			}
		}
	}
}

func (d *Dispatcher) lookup(id string) (Record, *runState, error) {
	runID := id
	if i := strings.IndexByte(runID, '#'); i >= 0 {
		runID = runID[:i]
	}
	d.mu.Lock()
	state := d.runs[runID]
	d.mu.Unlock()
	if state != nil {
		return d.stateRecord(state), state, nil
	}
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		rec, err := LoadOne(filepath.Dir(recorder.dir), runID)
		if err == nil {
			return rec, nil, nil
		}
	}
	return Record{}, nil, errors.New("not found")
}

func (d *Dispatcher) Get(id string) (Record, error) { rec, _, err := d.lookup(id); return rec, err }

// Collect marks a terminal record as observed by a caller. The collection
// hook is best-effort observability cleanup and must never affect the result.
func (d *Dispatcher) Collect(rec Record) Record {
	unlock := d.serialLocks([]string{rec.ID})
	defer unlock()
	if rec.Status.Terminal() && rec.Isolation == "worktree" {
		d.waitDone(rec.ID)
		rec = d.cleanupWorktree(rec.ID)
	}
	d.collect(rec)
	return rec
}

func (d *Dispatcher) collect(rec Record) {
	if d.onCollect != nil {
		d.onCollect(rec)
	}
}

// Records returns the persisted records where available, otherwise the
// dispatcher-owned in-memory records. It supports periodic housekeeping.
func (d *Dispatcher) Records() []Record {
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		records, err := Load(filepath.Dir(recorder.dir))
		if err == nil {
			return records
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	records := make([]Record, 0, len(d.runs))
	for _, state := range d.runs {
		records = append(records, cloneRecord(state.record))
	}
	return records
}

// Prune re-applies the recorder's retention policy during daemon housekeeping.
func (d *Dispatcher) Prune() {
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		recorder.prune(RecordsKept)
	}
}

func (d *Dispatcher) Cancel(id string) (Record, error) {
	rec, state, err := d.lookup(id)
	if err != nil {
		return Record{}, fmt.Errorf("unknown dispatch %s", id)
	}
	if state == nil {
		return rec, nil
	}
	if rec.Isolation == "worktree" {
		return d.terminateReapAndCleanup(id, StatusCanceled)
	}
	if rec.Status.Terminal() {
		return rec, nil
	}
	return d.terminate(id, StatusCanceled)
}

// terminateReapAndCleanup is the only cancellation path that can remove an
// isolated worktree. Serialization prevents another collector from observing
// a terminal status between termination and process reaping.
func (d *Dispatcher) terminateReapAndCleanup(id string, status Status) (Record, error) {
	unlock := d.serialLocks([]string{id})
	defer unlock()
	rec, state, err := d.lookup(id)
	if err != nil {
		return Record{}, fmt.Errorf("unknown dispatch %s", id)
	}
	if state == nil {
		return rec, nil
	}
	if !rec.Status.Terminal() {
		rec, err = d.terminate(id, status)
		if err != nil {
			return rec, err
		}
	}
	d.waitDone(strings.SplitN(id, "#", 2)[0])
	// Once terminal is published the run no longer owns its former numeric
	// PGID. A terminal Cancel therefore only retries cleanup; uncertainty is
	// retained rather than signalling a potentially recycled group.
	return d.cleanupWorktree(rec.ID), nil
}

func (d *Dispatcher) terminate(id string, status Status) (Record, error) {
	rec, state, err := d.lookup(id)
	if err != nil || state == nil || rec.Status.Terminal() {
		return rec, err
	}
	return d.terminateState(state, status), nil
}

// terminateState applies a cancellation to a run that was observed as live.
// The state can finish between that observation and the lock acquisition, so
// it must always re-check the authoritative record while locked.
func (d *Dispatcher) terminateState(state *runState, status Status) Record {
	d.mu.Lock()
	if state.record.Mode == ModeInteractive {
		pane, rt, paneRec := state.record.PaneID, d.interactiveRuntime, cloneRecord(state.record)
		d.mu.Unlock()
		// Cancellation kills first. Publishing settling first would allow a
		// concurrent hook to observe a partially torn-down session.
		if pane != "" && rt != nil && rt.Alive(pane) {
			paneRec, _ = d.closeRecordedPane(paneRec, pane, rt.Kill, runtimeLayout(rt))
		}
		d.mu.Lock()
		if state.record.Status.Terminal() {
			d.mu.Unlock()
			return paneRec
		}
		if !state.record.Status.Terminal() {
			d.beginSettlementLocked(state, status, 0)
			d.finishInteractiveLocked(state, status)
		}
		rec := state.record
		d.mu.Unlock()
		return rec
	}
	if state.record.Status.Terminal() {
		rec := state.record
		d.mu.Unlock()
		return rec
	}
	state.record.Status = status
	state.record.EndedAt = d.now()
	state.record.foldActive(state.record.EndedAt)
	turnID := ""
	if state.record.Mode == ModeHeadless && len(state.record.Turns) > 0 {
		t := &state.record.Turns[len(state.record.Turns)-1]
		t.EndedAt, t.Outcome, t.Status = state.record.EndedAt, TurnInterrupted, status
		t.Error = state.record.Error
		turnID = t.TurnID
	}
	d.completionCandidateLocked(state, transitionKey(state.record.ID, state.record.Mode, turnID), status)
	d.persistRecordLocked(state)
	rec := cloneRecord(state.record)
	done, cancel := state.done, state.cancel
	cancel = d.currentInvocationCancelLocked(state, done, cancel)
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return rec
}

func (d *Dispatcher) currentInvocationCancelLocked(state *runState, done chan struct{}, cancel context.CancelFunc) context.CancelFunc {
	if state.done == done {
		return cancel
	}
	return nil
}

// Consult preserves the synchronous one-off consultant API over dispatch.
func (d *Dispatcher) Consult(ctx context.Context, cfg *config.Config, req Request) (Result, error) {
	req.Kind, req.Preamble = "consult", true
	started, err := d.Start(ctx, cfg, req)
	if err != nil {
		return Result{}, err
	}
	entries := d.Wait(ctx, []string{started.ID}, RunTimeout)
	if err := ctx.Err(); err != nil {
		status := StatusCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			status = StatusTimeout
		}
		if req.Isolation == "worktree" {
			_, _ = d.terminateReapAndCleanup(started.ID, status)
		} else {
			_, _ = d.terminate(started.ID, status)
			d.waitDone(started.ID)
		}
		return Result{}, err
	}
	entry := entries[0]
	if entry.Status != StatusDone {
		return Result{}, errors.New(entry.Err)
	}
	return Result{ID: started.ID, Harness: started.Harness, Model: started.Model, Text: entry.Text}, nil
}

func (d *Dispatcher) waitDone(id string) {
	if d.waitDoneHook != nil {
		d.waitDoneHook(id)
	}
	d.mu.Lock()
	state := d.runs[id]
	d.mu.Unlock()
	if state == nil {
		return
	}
	<-state.done
}

// MarkInterrupted marks persisted in-flight dispatches as failed after a
// daemon restart. In-memory runs belong to this process and are untouched.
func (d *Dispatcher) MarkInterrupted() {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return
	}
	markedAt := d.now()
	for _, rec := range func() []Record { records, _ := Load(filepath.Dir(recorder.dir)); return records }() {
		rec = d.reconcileRestartViewer(rec)
		if rec.WorktreeState == WorktreeCreating {
			rec = d.reconcileCreating(rec)
		}
		if rec.Status.Terminal() {
			writerUncertain := false
			if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved && rec.Mode != ModeInteractive {
				writerUncertain = true
			}
			if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved && rec.Mode == ModeInteractive && rec.PaneID == "" {
				writerUncertain = true
			}
			if rec.Mode == ModeInteractive && rec.PaneID != "" && d.interactiveRuntime != nil {
				alive, certain := panePresent(d.interactiveRuntime, rec.PaneID)
				if !certain && rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved {
					writerUncertain = true
				} else if alive {
					cleaned, killErr := d.closeRestartPane(rec)
					if killErr != nil {
						d.trackRestartKill(rec)
						if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved {
							writerUncertain = true
						}
					} else {
						rec = cleaned
						_ = writeRecord(recorder.dir, rec)
					}
				}
			}
			if writerUncertain {
				rec = d.setWorktreeState(rec, nil, WorktreeKept)
			} else {
				rec = d.cleanupWorktree(rec.ID)
			}
			d.restorePendingNotifications(rec)
			continue
		}
		rec.Error, rec.EndedAt = "daemon restarted", markedAt
		if rec.Mode == ModeInteractive {
			finished := false
			for i := range rec.Turns {
				if rec.Turns[i].Outcome == TurnFinished {
					finished = true
				}
				if rec.Turns[i].Outcome == "" {
					rec.Turns[i].Outcome = TurnLost
					rec.Turns[i].EndedAt = markedAt
					rec.Turns[i].SlotHeld = false
				}
			}
			if finished {
				rec.Status = StatusClosed
			} else {
				rec.Status = StatusFailed
			}
		} else {
			if rec.Mode == "" {
				rec.Mode = ModeHeadless
			}
			rec.Status = StatusFailed
			if len(rec.Turns) == 0 {
				synthesizeOpeningTurn(&rec)
			}
			for i := range rec.Turns {
				if rec.Turns[i].Outcome == "" {
					rec.Turns[i].Outcome, rec.Turns[i].Status = TurnInterrupted, StatusFailed
					rec.Turns[i].Error, rec.Turns[i].EndedAt = "daemon restarted", markedAt
					rec.Turns[i].SlotHeld = false
				}
			}
			if rec.Isolation == "worktree" && rec.WorktreeState != WorktreeRemoved {
				rec.WorktreeState = WorktreeKept
			}
		}
		rec.foldActive(markedAt)
		d.mu.Lock()
		d.addRestartCandidates(&rec)
		d.mu.Unlock()
		if rec.Mode == ModeInteractive && rec.PaneID != "" && d.interactiveRuntime != nil {
			alive, certain := panePresent(d.interactiveRuntime, rec.PaneID)
			if !certain {
				if rec.Isolation == "worktree" {
					rec.WorktreeState = WorktreeKept
				}
			} else if alive {
				cleaned, killErr := d.closeRestartPane(rec)
				if killErr != nil {
					d.trackRestartKill(rec)
				} else {
					rec = cleaned
				}
			}
		}
		if err := writeRecord(recorder.dir, rec); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: recording: %v\n", rec.ID, err)
		}
		if rec.Status.Terminal() && rec.WorktreeState != WorktreeKept {
			rec = d.cleanupWorktree(rec.ID)
		}
		d.restorePendingNotifications(rec)
	}
}

func (d *Dispatcher) closeRestartPane(rec Record) (Record, error) {
	rt := d.interactiveRuntime
	return d.closeRecordedPane(rec, rec.PaneID, rt.Kill, runtimeLayout(rt))
}

// trackRestartKill retains a terminal record solely to retry cleanup. Terminal
// records normally prune immediately, but a failed kill must not strand its pane.
func (d *Dispatcher) trackRestartKill(rec Record) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs[rec.ID] == nil {
		var handle Handle = nopHandle{}
		if recorder, ok := d.recorder.(*FileRecorder); ok {
			handle = &restoredNotificationHandle{dir: recorder.dir, rec: rec}
		}
		done := make(chan struct{})
		close(done)
		d.runs[rec.ID] = &runState{record: rec, handle: handle, done: done, killPending: true}
		return
	}
	d.runs[rec.ID].killPending = true
}

// finish closes a recording. A recording failure is reported to the daemon
// log rather than returned: it must not turn a good answer into an error,
// nor mask the failure that actually ended the consult.
func finish(handle Handle, id string, status Status, cause error) {
	if err := handle.Close(status, cause); err != nil {
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", id, err)
	}
}

// recordingTee fans the harness's output into an in-memory buffer, parsed
// for the final result, and the consult's recording, read back live by
// `leo consult watch`.
//
// The mutex is redundant while this value is assigned to both cmd.Stdout
// and cmd.Stderr (os/exec then copies on a single goroutine) and is kept
// deliberately: swapping in an io.MultiWriter here would otherwise
// reintroduce a data race silently. See internal/run/runner.go's syncBuffer
// for the case where exactly that happened.
type recordingTee struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	handle  Handle
	usage   harness.UsageAccumulator
	pending []byte
	onUsage func(*harness.Usage)
}

func (t *recordingTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf.Write(p)
	var snapshots []*harness.Usage
	if t.usage != nil {
		t.pending = append(t.pending, p...)
		for {
			i := bytes.IndexByte(t.pending, '\n')
			if i < 0 {
				break
			}
			line := bytes.TrimSpace(t.pending[:i])
			t.pending = t.pending[i+1:]
			if len(line) > 0 {
				t.usage.AddLine(line)
				snapshots = append(snapshots, t.usage.Usage())
			}
		}
	}
	// Recording is best-effort; the handle reports failures from Close.
	_, _ = t.handle.Write(p)
	t.mu.Unlock()
	// Persisting can perform file I/O; do it after releasing the tee mutex so
	// a recorder callback can never block a concurrent stdout/stderr writer.
	if t.onUsage != nil {
		for _, usage := range snapshots {
			t.onUsage(usage)
		}
	}
	return len(p), nil
}

// Bytes returns a copy. Handing back the buffer's live slice under the
// lock would be false comfort: the caller would read it unlocked, so the
// very concurrency the mutex above guards against would still corrupt it.
func (t *recordingTee) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bytes.Clone(t.buf.Bytes())
}

func mergedEnv(base []string, overlays ...map[string]string) []string {
	values := make(map[string]string, len(base))
	order := make([]string, 0, len(base))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, overlay := range overlays {
		for key, value := range overlay {
			if _, exists := values[key]; !exists {
				order = append(order, key)
			}
			values[key] = value
		}
	}
	out := make([]string, 0, len(values))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}
