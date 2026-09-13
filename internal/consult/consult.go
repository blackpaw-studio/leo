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
)

const (
	// RunTimeout is the authoritative deadline for one consult. A consult is
	// a full agent run, so this is generous; it stays a hard cap only so a
	// wedged harness process can't hold one of maxConcurrent slots forever.
	// Harness-side MCP tool ceilings are derived from it (leomcp.ToolTimeout)
	// so leo, not the coding agent, is what times a consult out.
	RunTimeout    = 30 * time.Minute
	maxConcurrent = 4
	preamble      = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."
)

type Request struct {
	Template string
	Model    string
	Prompt   string
	Cwd      string
	Name     string
	Kind     string
	Preamble bool
	// Caller names the process that asked, for the consult record. Optional.
	Caller string
}

type Result struct {
	// ID identifies the consult's record and event stream, so a caller can
	// point at it after the fact (`leo consult watch <id>`).
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Text    string `json:"text"`
}

type Started struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Cwd     string `json:"cwd"`
}

type Entry struct {
	ID      string        `json:"id"`
	Status  Status        `json:"status"`
	Elapsed time.Duration `json:"elapsed"`
	Text    string        `json:"text,omitempty"`
	Err     string        `json:"error,omitempty"`
}

// ValidationError reports a request/configuration problem that should be
// returned to API clients as a 4xx response rather than an execution failure.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func invalidf(format string, args ...any) error {
	return &ValidationError{Err: fmt.Errorf(format, args...)}
}

type Dispatcher struct {
	sem                chan struct{}
	recorder           Recorder
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
	daemonCtx          context.Context
	mu                 sync.Mutex
	runs               map[string]*runState
}

type runState struct {
	record Record
	handle Handle
	done   chan struct{}
	cancel context.CancelFunc
}

// NewDispatcher builds a dispatcher recording through rec. A nil recorder
// discards recordings, leaving behavior exactly as it was before consults
// were observable.
func NewDispatcher(rec Recorder) *Dispatcher {
	if rec == nil {
		rec = nopRecorder{}
	}
	return &Dispatcher{
		sem:                make(chan struct{}, maxConcurrent),
		recorder:           rec,
		ExecCommandContext: exec.CommandContext,
		daemonCtx:          context.Background(),
		runs:               make(map[string]*runState),
	}
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
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Started{}, invalidf("model for consult: %v", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Started{}, invalidf("template %q harness_options: %v", req.Template, err)
	}
	if req.Cwd == "" || !filepath.IsAbs(req.Cwd) {
		return Started{}, invalidf("cwd must be an existing absolute directory")
	}
	info, err := os.Stat(req.Cwd)
	if err != nil || !info.IsDir() {
		return Started{}, invalidf("cwd must be an existing absolute directory")
	}

	spec := harness.LaunchSpec{
		Kind: harness.KindTask, Name: req.Name, Model: model,
		MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Cwd,
		Prompt: requestPrompt(req), Options: decoded,
	}
	if spec.Name == "" {
		spec.Name = "dispatch"
	}
	args, err := h.Args(spec)
	if err != nil {
		return Started{}, invalidf("building %s args: %v", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return Started{}, invalidf("building %s env: %v", h.Name(), err)
	}

	// Record before competing for a slot, so consults waiting behind the
	// concurrency limit are visible too. Validation failures never ran and
	// are deliberately not recorded.
	rec := Record{
		ID: newID(), Caller: req.Caller, Template: req.Template,
		Kind: requestKind(req), Harness: h.Name(), Model: model, Cwd: req.Cwd, Name: req.Name,
		Prompt: req.Prompt, Status: StatusQueued, StartedAt: time.Now(),
	}
	handle, err := d.recorder.Open(rec)
	if err != nil {
		// Recording is best-effort. An unwritable state directory should
		// cost visibility, not the answer the caller is waiting for.
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", rec.ID, err)
		handle = nopHandle{}
	}
	runCtx, cancel := context.WithCancel(d.daemonCtx)
	state := &runState{record: rec, handle: handle, done: make(chan struct{}), cancel: cancel}
	d.mu.Lock()
	d.runs[rec.ID] = state
	d.mu.Unlock()
	go d.run(runCtx, state, h, model, tmpl.Env, args, harnessEnv, req.Cwd)
	return Started{ID: rec.ID, Harness: h.Name(), Model: model, Cwd: req.Cwd}, nil
}

func requestKind(req Request) string {
	if req.Kind != "" {
		return req.Kind
	}
	return "dispatch"
}

func requestPrompt(req Request) string {
	if req.Preamble {
		return preamble + "\n\n" + req.Prompt
	}
	return req.Prompt
}

func (d *Dispatcher) run(parent context.Context, state *runState, h harness.Harness, model string, env map[string]string, args []string, harnessEnv map[string]string, cwd string) {
	defer close(state.done)
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-parent.Done():
		d.complete(state, StatusCanceled, "", parent.Err())
		return
	}
	// A queued run can be canceled at the same instant a concurrency slot
	// opens. Do not publish a misleading running transition in that race.
	if err := parent.Err(); err != nil {
		d.complete(state, StatusCanceled, "", err)
		return
	}
	if err := state.handle.SetStatus(StatusRunning); err != nil {
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", state.record.ID, err)
	}
	d.setStatus(state, StatusRunning)
	runCtx, timeoutCancel := context.WithTimeout(parent, RunTimeout)
	defer timeoutCancel()
	binary := h.Binary()
	if p, err := exec.LookPath(binary); err == nil {
		binary = p
	}
	cmd := d.ExecCommandContext(runCtx, binary, args...)
	cmd.Dir = cwd
	cmd.Env = mergedEnv(os.Environ(), harnessEnv, env)
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
	cmd.Stdout, cmd.Stderr = tee, tee

	runErr := cmd.Run()
	parsed, parseErr := h.ParseEvents(bytes.NewReader(tee.Bytes()))
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

func (d *Dispatcher) setStatus(state *runState, status Status) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state.record.Status.Terminal() {
		return
	}
	state.record.Status = status
}

func (d *Dispatcher) complete(state *runState, status Status, text string, cause error) {
	d.mu.Lock()
	if state.record.Status.Terminal() {
		status = state.record.Status
	}
	state.record.Status, state.record.Text, state.record.EndedAt = status, text, time.Now()
	if cause != nil {
		state.record.Error = cause.Error()
	}
	d.mu.Unlock()
	if text != "" {
		_ = state.handle.SetText(text)
	}
	finish(state.handle, state.record.ID, status, cause)
}

// Wait returns one entry per requested dispatch once all are terminal or the
// supplied timeout expires. It never polls records.
func (d *Dispatcher) Wait(ctx context.Context, ids []string, timeout time.Duration) []Entry {
	entries := make([]Entry, len(ids))
	states := make([]*runState, len(ids))
	for i, id := range ids {
		entries[i].ID = id
		rec, state, err := d.lookup(id)
		if err != nil {
			entries[i].Err = fmt.Sprintf("unknown dispatch %s", id)
			continue
		}
		states[i] = state
		entries[i] = entryFromRecord(rec)
	}
	deadline := time.NewTimer(timeout)
	if timeout <= 0 {
		deadline.Stop()
	}
	defer deadline.Stop()
	for {
		pending := false
		cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}}
		if timeout > 0 {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(deadline.C)})
		}
		for i, state := range states {
			if state != nil && !entries[i].Status.Terminal() {
				pending = true
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(state.done)})
			}
		}
		if !pending {
			return entries
		}
		chosen, _, _ := reflect.Select(cases)
		if chosen == 0 || (timeout > 0 && chosen == 1) {
			for i, state := range states {
				if state != nil && !entries[i].Status.Terminal() {
					entries[i] = entryFromRecord(d.stateRecord(state))
					if !entries[i].Status.Terminal() {
						entries[i].Status = StatusRunning
						entries[i].Err = ""
					}
				}
			}
			return entries
		}
		for i, state := range states {
			if state != nil {
				entries[i] = entryFromRecord(d.stateRecord(state))
			}
		}
	}
}

func entryFromRecord(rec Record) Entry {
	return Entry{ID: rec.ID, Status: rec.Status, Elapsed: rec.Elapsed(time.Now()), Text: rec.Text, Err: rec.Error}
}

func (d *Dispatcher) stateRecord(state *runState) Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return state.record
}

func (d *Dispatcher) lookup(id string) (Record, *runState, error) {
	d.mu.Lock()
	state := d.runs[id]
	d.mu.Unlock()
	if state != nil {
		return d.stateRecord(state), state, nil
	}
	if recorder, ok := d.recorder.(*FileRecorder); ok {
		rec, err := LoadOne(filepath.Dir(recorder.dir), id)
		if err == nil {
			return rec, nil, nil
		}
	}
	return Record{}, nil, errors.New("not found")
}

func (d *Dispatcher) Get(id string) (Record, error) { rec, _, err := d.lookup(id); return rec, err }

func (d *Dispatcher) Cancel(id string) (Record, error) {
	rec, state, err := d.lookup(id)
	if err != nil {
		return Record{}, fmt.Errorf("unknown dispatch %s", id)
	}
	if rec.Status.Terminal() {
		return rec, nil
	}
	if state == nil {
		return rec, nil
	}
	return d.terminate(id, StatusCanceled)
}

func (d *Dispatcher) terminate(id string, status Status) (Record, error) {
	rec, state, err := d.lookup(id)
	if err != nil || state == nil || rec.Status.Terminal() {
		return rec, err
	}
	d.mu.Lock()
	state.record.Status = status
	state.record.EndedAt = time.Now()
	rec = state.record
	d.mu.Unlock()
	_ = state.handle.SetStatus(status)
	state.cancel()
	return rec, nil
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
		_, _ = d.terminate(started.ID, status)
		d.waitDone(started.ID)
		return Result{}, err
	}
	entry := entries[0]
	if entry.Status != StatusDone {
		return Result{}, errors.New(entry.Err)
	}
	return Result{ID: started.ID, Harness: started.Harness, Model: started.Model, Text: entry.Text}, nil
}

func (d *Dispatcher) waitDone(id string) {
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
	for _, rec := range func() []Record { records, _ := Load(filepath.Dir(recorder.dir)); return records }() {
		if rec.Status.Terminal() {
			continue
		}
		rec.Status, rec.Error, rec.EndedAt = StatusFailed, "daemon restarted", time.Now()
		if err := writeRecord(recorder.dir, rec); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: recording: %v\n", rec.ID, err)
		}
	}
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
	mu     sync.Mutex
	buf    bytes.Buffer
	handle Handle
}

func (t *recordingTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	// Recording is best-effort; the handle reports failures from Close.
	_, _ = t.handle.Write(p)
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
