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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

const (
	RunTimeout    = 10 * time.Minute
	maxConcurrent = 4
	preamble      = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."
)

type Request struct {
	Template  string
	Model     string
	Prompt    string
	Workspace string
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
	}
}

// newID mints a consult id short enough to type and unique enough for the
// handful of records retained.
func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "c-" + hex.EncodeToString(b[:])
}

// Consult validates and executes a consultant synchronously. The caller's
// context controls queueing and execution; RunTimeout supplies an upper bound.
func (d *Dispatcher) Consult(ctx context.Context, cfg *config.Config, req Request) (Result, error) {
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		return Result{}, invalidf("unknown template %q", req.Template)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return Result{}, invalidf("resolving harness for template %q: %v", req.Template, err)
	}
	if !h.SupportsKind(harness.KindTask) {
		return Result{}, invalidf("harness %q does not support one-shot runs", h.Name())
	}
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Result{}, invalidf("model for consult: %v", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Result{}, invalidf("template %q harness_options: %v", req.Template, err)
	}

	spec := harness.LaunchSpec{
		Kind: harness.KindTask, Name: "consult", Model: model,
		MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Workspace,
		Prompt: preamble + "\n\n" + req.Prompt, Options: decoded,
	}
	args, err := h.Args(spec)
	if err != nil {
		return Result{}, invalidf("building %s args: %v", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return Result{}, invalidf("building %s env: %v", h.Name(), err)
	}

	// Record before competing for a slot, so consults waiting behind the
	// concurrency limit are visible too. Validation failures never ran and
	// are deliberately not recorded.
	rec := Record{
		ID: newID(), Caller: req.Caller, Template: req.Template,
		Harness: h.Name(), Model: model, Workspace: req.Workspace,
		Prompt: req.Prompt, Status: StatusQueued, StartedAt: time.Now(),
	}
	handle, err := d.recorder.Open(rec)
	if err != nil {
		return Result{}, fmt.Errorf("recording consult: %w", err)
	}
	fail := func(status Status, err error) (Result, error) {
		finish(handle, rec.ID, status, err)
		return Result{}, err
	}

	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return fail(StatusCanceled, ctx.Err())
	}
	if err := handle.SetStatus(StatusRunning); err != nil {
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", rec.ID, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	binary := h.Binary()
	if p, err := exec.LookPath(binary); err == nil {
		binary = p
	}
	cmd := d.ExecCommandContext(runCtx, binary, args...)
	cmd.Dir = req.Workspace
	cmd.Env = mergedEnv(os.Environ(), harnessEnv, tmpl.Env)
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
	tee := &recordingTee{handle: handle}
	cmd.Stdout, cmd.Stderr = tee, tee

	runErr := cmd.Run()
	parsed, parseErr := h.ParseEvents(bytes.NewReader(tee.Bytes()))
	if runCtx.Err() != nil {
		status := StatusTimeout
		if errors.Is(runCtx.Err(), context.Canceled) {
			status = StatusCanceled
		}
		return fail(status, fmt.Errorf("consult %s/%s: %w", h.Name(), model, runCtx.Err()))
	}
	if runErr != nil {
		detail := runErr.Error()
		if len(parsed.Errors) > 0 {
			detail += ": " + parsed.Errors[0]
		}
		return fail(StatusFailed, fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail))
	}
	if parseErr != nil {
		return fail(StatusFailed, fmt.Errorf("consult %s/%s returned unreadable output: %w", h.Name(), model, parseErr))
	}
	if parsed.IsError {
		detail := "consultant reported an error"
		if len(parsed.Errors) > 0 {
			detail = strings.Join(parsed.Errors, "; ")
		}
		return fail(StatusFailed, fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail))
	}
	if parsed.Text == "" {
		return fail(StatusFailed, fmt.Errorf("consult %s/%s produced no output", h.Name(), model))
	}
	finish(handle, rec.ID, StatusDone, nil)
	return Result{ID: rec.ID, Harness: h.Name(), Model: model, Text: parsed.Text}, nil
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

func (t *recordingTee) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Bytes()
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
