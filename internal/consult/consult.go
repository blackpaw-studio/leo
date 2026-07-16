// Package consult runs one-off headless "second opinion" subagents and
// returns their final text synchronously to the caller.
package consult

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
}

type Result struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Text    string `json:"text"`
}

type Dispatcher struct {
	sem                chan struct{}
	ExecCommandContext func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{sem: make(chan struct{}, maxConcurrent), ExecCommandContext: exec.CommandContext}
}

// Consult validates and executes a consultant synchronously. The caller's
// context controls queueing and execution; RunTimeout supplies an upper bound.
func (d *Dispatcher) Consult(ctx context.Context, cfg *config.Config, req Request) (Result, error) {
	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		return Result{}, fmt.Errorf("unknown template %q", req.Template)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return Result{}, fmt.Errorf("resolving harness for template %q: %w", req.Template, err)
	}
	if !h.SupportsKind(harness.KindTask) {
		return Result{}, fmt.Errorf("harness %q does not support one-shot runs", h.Name())
	}
	model := req.Model
	if model == "" {
		model = cfg.TemplateModel(tmpl)
	}
	if err := h.ValidateModel(model); err != nil {
		return Result{}, fmt.Errorf("model for consult: %w", err)
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return Result{}, fmt.Errorf("template %q harness_options: %w", req.Template, err)
	}

	spec := harness.LaunchSpec{
		Kind: harness.KindTask, Name: "consult", Model: model,
		MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: req.Workspace,
		Prompt: preamble + "\n\n" + req.Prompt, Options: decoded,
	}
	args, err := h.Args(spec)
	if err != nil {
		return Result{}, fmt.Errorf("building %s args: %w", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return Result{}, fmt.Errorf("building %s env: %w", h.Name(), err)
	}

	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
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

	out, runErr := cmd.CombinedOutput()
	parsed, parseErr := h.ParseEvents(bytes.NewReader(out))
	if runCtx.Err() != nil {
		return Result{}, fmt.Errorf("consult %s/%s: %w", h.Name(), model, runCtx.Err())
	}
	if runErr != nil {
		detail := runErr.Error()
		if len(parsed.Errors) > 0 {
			detail += ": " + parsed.Errors[0]
		}
		return Result{}, fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail)
	}
	if parseErr != nil {
		return Result{}, fmt.Errorf("consult %s/%s returned unreadable output: %w", h.Name(), model, parseErr)
	}
	if parsed.IsError {
		detail := "consultant reported an error"
		if len(parsed.Errors) > 0 {
			detail = strings.Join(parsed.Errors, "; ")
		}
		return Result{}, fmt.Errorf("consult %s/%s failed: %s", h.Name(), model, detail)
	}
	if parsed.Text == "" {
		return Result{}, fmt.Errorf("consult %s/%s produced no output", h.Name(), model)
	}
	return Result{Harness: h.Name(), Model: model, Text: parsed.Text}, nil
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
