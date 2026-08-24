package picker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// SSHBackend dispatches list/lifecycle operations to a remote leo binary over
// SSH. The sshArgs closure builds the full ssh argv (host + configured ssh args
// + ControlMaster opts) for a given remote command tail; it is injected by the
// cli layer so all SSH multiplexing policy stays there. exec is a seam so tests
// never touch a real ssh.
type SSHBackend struct {
	host     string
	leoPath  string
	tmuxPath string
	sshArgs  func(tail ...string) []string
	exec     func(name string, args ...string) *exec.Cmd
}

// NewSSHBackend builds an SSH backend for a named host. leoPath/tmuxPath are the
// remote binary paths (e.g. config.HostConfig.RemoteLeoPath()).
func NewSSHBackend(host, leoPath, tmuxPath string, sshArgs func(tail ...string) []string) *SSHBackend {
	return &SSHBackend{
		host:     host,
		leoPath:  leoPath,
		tmuxPath: tmuxPath,
		sshArgs:  sshArgs,
		exec:     exec.Command,
	}
}

// shellQuoteArg wraps a value in single quotes, escaping embedded single quotes.
// ssh flattens everything after the host into one string handed to the remote
// login shell (which re-parses it), so any argument that must survive that
// re-parse intact — an agent name, a tmux format — has to be single-token
// quoted. This mirrors internal/cli.shellQuoteArg; the picker cannot import cli
// (that would be an import cycle), so the one-liner is duplicated here.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// List fetches the remote agents via `leo agent list --json`. On failure it
// degrades to `tmux -L leo list-sessions`, returning attach-only rows so an old
// remote leo (predating --json) or a partial outage still shows something.
func (b *SSHBackend) List(ctx context.Context) ([]Agent, error) {
	out, err := b.run(ctx, b.leoPath, "agent", "list", "--json")
	if err != nil {
		return b.listViaTmux(ctx)
	}
	var records []agent.Record
	if jsonErr := json.Unmarshal(out, &records); jsonErr != nil {
		return b.listViaTmux(ctx)
	}
	agents := make([]Agent, 0, len(records))
	for _, r := range records {
		agents = append(agents, Agent{
			Name:      r.Name,
			Template:  r.Template,
			Host:      b.host,
			Status:    r.Status,
			StartedAt: r.StartedAt,
		})
	}
	return agents, nil
}

// listViaTmux enumerates leo- sessions on the remote and returns attach-only
// rows. The format arg is single-quoted so the `#` cannot start a remote shell
// comment.
func (b *SSHBackend) listViaTmux(ctx context.Context) ([]Agent, error) {
	tail := append([]string{b.tmuxPath}, tmux.Args("list-sessions", "-F", shellQuoteArg("#{session_name}"))...)
	out, err := b.run(ctx, tail...)
	if err != nil {
		return nil, fmt.Errorf("listing remote sessions on %s: %w", b.host, err)
	}
	var agents []Agent
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, "leo-") {
			continue
		}
		agents = append(agents, Agent{
			Name:       name,
			Host:       b.host,
			Status:     "running",
			AttachOnly: true,
		})
	}
	return agents, nil
}

func (b *SSHBackend) Rename(ctx context.Context, oldName, newName string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "rename", shellQuoteArg(oldName), shellQuoteArg(newName))
	return err
}

func (b *SSHBackend) Stop(ctx context.Context, name string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "stop", shellQuoteArg(name))
	return err
}

func (b *SSHBackend) Start(ctx context.Context, name string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "start", shellQuoteArg(name))
	return err
}

// DeletePlan shells `leo agent delete-plan <name>` on the remote — the
// hidden plumbing subcommand that prints agent.DeletePlan as JSON — so the
// remote picker row can render the same confirm text as the local backend
// without a bespoke second protocol over SSH.
func (b *SSHBackend) DeletePlan(ctx context.Context, name string) (agent.DeletePlan, error) {
	out, err := b.run(ctx, b.leoPath, "agent", "delete-plan", shellQuoteArg(name))
	if err != nil {
		return agent.DeletePlan{}, fmt.Errorf("planning delete on %s: %w", b.host, err)
	}
	var plan agent.DeletePlan
	if err := json.Unmarshal(out, &plan); err != nil {
		return agent.DeletePlan{}, fmt.Errorf("decoding delete plan from %s: %w", b.host, err)
	}
	return plan, nil
}

func (b *SSHBackend) Delete(ctx context.Context, name string, deleteBranch bool) error {
	args := []string{b.leoPath, "agent", "delete", shellQuoteArg(name), "--yes"}
	if deleteBranch {
		args = append(args, "--delete-branch")
	}
	_, err := b.run(ctx, args...)
	return err
}

// Templates fetches the remote's configured template names via
// `leo template list --json`. Only the name field is decoded — the menu shows
// names, and pinning the rest of that payload's shape here would couple the
// picker to a CLI struct it cannot import.
func (b *SSHBackend) Templates(ctx context.Context) ([]string, error) {
	out, err := b.run(ctx, b.leoPath, "template", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("listing templates on %s: %w", b.host, err)
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("decoding templates from %s: %w", b.host, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name != "" {
			names = append(names, e.Name)
		}
	}
	return names, nil
}

func (b *SSHBackend) SwitchTemplate(ctx context.Context, name, template string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "set-template", shellQuoteArg(name), shellQuoteArg(template))
	return err
}

// run executes `ssh <args...> <tail...>` and returns stdout, wrapping stderr on
// failure.
func (b *SSHBackend) run(ctx context.Context, tail ...string) ([]byte, error) {
	args := b.sshArgs(tail...)
	cmd := b.exec("ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
