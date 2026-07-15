// Package consult dispatches one-off headless "second opinion" subagents:
// a single harness -p style run on a template's harness/model, whose final
// text is delivered back to the calling agent as an injected message.
package consult

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

const (
	// runTimeout bounds one consultant run end to end.
	runTimeout = 10 * time.Minute
	// deliverTimeout bounds reply injection (readiness probe + possible
	// caller resume — a cold-booting claude can take ~60s).
	deliverTimeout = 3 * time.Minute
	// maxConcurrent caps simultaneously running consults so a council
	// fan-out can't fork-bomb a local model server. Excess dispatches
	// queue; they are never rejected.
	maxConcurrent = 4
	// preamble frames the consultant's role. Prepended to the prompt (not
	// SystemContext) so it reaches every harness, including opencode.
	preamble = "You are a one-off consultant: another agent is asking for your independent opinion. Analyze and answer directly and completely in your final message. Do not modify any files or take actions beyond reading. The question follows."
)

// Request describes one consult dispatch.
type Request struct {
	From      string // calling agent name; the reply target
	Template  string // template supplying harness/model/env/harness_options
	Model     string // optional model override
	Prompt    string // self-contained question
	Workspace string // caller's workspace (resolved by the caller of Dispatch)
}

// Ticket identifies an accepted consult.
type Ticket struct {
	ID      string
	Harness string
	Model   string
}

// DeliverFunc injects the framed reply body into the named agent's session.
type DeliverFunc func(ctx context.Context, agentName, body string) error

// Dispatcher validates and runs consults. Safe for concurrent use.
type Dispatcher struct {
	deliver DeliverFunc
	sem     chan struct{}

	// ExecCommandContext is the exec seam; tests replace it.
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewDispatcher builds a Dispatcher that hands replies to deliver.
func NewDispatcher(deliver DeliverFunc) *Dispatcher {
	return &Dispatcher{
		deliver:            deliver,
		sem:                make(chan struct{}, maxConcurrent),
		ExecCommandContext: exec.CommandContext,
	}
}

// Dispatch validates the consult synchronously and launches it in the
// background. The returned Ticket's ID appears in the eventual reply frame.
func (d *Dispatcher) Dispatch(cfg *config.Config, req Request) (Ticket, error) {
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		return Ticket{}, fmt.Errorf("unknown template %q", req.Template)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return Ticket{}, fmt.Errorf("resolving harness for template %q: %w", req.Template, err)
	}
	if !h.SupportsKind(harness.KindTask) {
		return Ticket{}, fmt.Errorf("harness %q does not support one-shot runs", h.Name())
	}
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Ticket{}, fmt.Errorf("model for consult: %w", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Ticket{}, fmt.Errorf("template %q harness_options: %w", req.Template, err)
	}

	id, err := newID()
	if err != nil {
		return Ticket{}, fmt.Errorf("generating consult id: %w", err)
	}

	// Runtime-only option fields (MCP wiring, channel prefixes) are left
	// zero deliberately: the consultant is advisory and gets no leo tools.
	spec := harness.LaunchSpec{
		Kind:      harness.KindTask,
		Name:      "consult-" + id,
		Model:     model,
		Workspace: req.Workspace,
		Prompt:    preamble + "\n\n" + req.Prompt,
		Options:   decoded,
	}
	args, err := h.Args(spec)
	if err != nil {
		return Ticket{}, fmt.Errorf("building %s args: %w", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return Ticket{}, fmt.Errorf("building %s env: %w", h.Name(), err)
	}

	tk := Ticket{ID: id, Harness: h.Name(), Model: model}
	go d.run(tk, h, req, args, mergeEnv(harnessEnv, tmpl.Env))
	return tk, nil
}

// run executes one consult and delivers its reply. Always delivers
// something — success text, error notice, or timeout notice.
func (d *Dispatcher) run(tk Ticket, h harness.Harness, req Request, args []string, extraEnv map[string]string) {
	d.sem <- struct{}{}
	defer func() { <-d.sem }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	binary := h.Binary()
	if p, err := exec.LookPath(binary); err == nil {
		binary = p
	}
	cmd := d.ExecCommandContext(ctx, binary, args...)
	cmd.Dir = req.Workspace
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	// A cancelled consultant may leave children holding the output pipes;
	// WaitDelay bounds how long Wait blocks on them after ctx fires.
	cmd.WaitDelay = 10 * time.Second

	out, runErr := cmd.CombinedOutput()
	elapsed := time.Since(start).Round(time.Second)

	res, parseErr := h.ParseEvents(bytes.NewReader(out))
	body := formatReply(tk, elapsed, res, ctx.Err(), runErr, parseErr)

	dctx, dcancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer dcancel()
	if err := d.deliver(dctx, req.From, body); err != nil {
		log.Printf("consult %s: delivering reply to %q failed: %v", tk.ID, req.From, err)
	}
}

// formatReply renders the framed reply. Failure detail preference order:
// timeout, run error (with stream errors if any), parse failure, empty text.
func formatReply(tk Ticket, elapsed time.Duration, res harness.Result, ctxErr, runErr, parseErr error) string {
	frame := fmt.Sprintf("[consult %s · %s/%s · %s]", tk.ID, tk.Harness, tk.Model, elapsed)
	fail := func(detail string) string {
		return fmt.Sprintf("[consult %s · %s/%s · failed after %s] %s", tk.ID, tk.Harness, tk.Model, elapsed, detail)
	}
	switch {
	case ctxErr != nil:
		return fail("timed out")
	case runErr != nil:
		detail := runErr.Error()
		if len(res.Errors) > 0 {
			detail += ": " + res.Errors[0]
		}
		return fail(detail)
	case parseErr != nil:
		return fail("unreadable output: " + parseErr.Error())
	case res.IsError:
		detail := "consultant reported an error"
		if len(res.Errors) > 0 {
			detail = res.Errors[0]
		}
		return fail(detail)
	case res.Text == "":
		return fail("consultant produced no output")
	}
	return frame + " " + res.Text
}

// mergeEnv overlays later maps onto earlier ones; nil when empty.
func mergeEnv(maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// newID returns a short random consult id like "c-4f2a".
func newID() (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "c-" + hex.EncodeToString(b), nil
}
