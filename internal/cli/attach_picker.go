package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/manifoldco/promptui"
)

// attachChoice is one row in the attach picker — a human label (what the
// user navigates through) plus the tmux session name that gets attached.
type attachChoice struct {
	label   string
	session string
}

// runAttachPicker handles `leo attach` with no positional arg. It enumerates
// candidate sessions (local processes + agents via the daemon, or remote
// sessions via `ssh <host> tmux -L leo list-sessions`), shows an arrow-key
// picker, and attaches to the selection. Errors cleanly when stdin is not a
// TTY so non-interactive callers don't hang.
func runAttachPicker(cfg *config.Config, res config.HostResolution, opts attachOptions) error {
	if !stdinIsTerminal() {
		return fmt.Errorf("no session name given and stdin is not a terminal — pass a name explicitly")
	}

	var (
		choices []attachChoice
		err     error
	)
	if res.Localhost {
		choices = localAttachChoices(cfg)
	} else {
		choices, err = remoteAttachChoices(res)
		if err != nil {
			return err
		}
	}
	if len(choices) == 0 {
		return fmt.Errorf("no attachable sessions found")
	}

	// Shorthand: a single candidate means we skip the picker — if the user
	// has one thing running there is nothing to choose between.
	if len(choices) == 1 {
		return attachTmuxSession(res, choices[0].session, opts)
	}

	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = c.label
	}
	selector := promptui.Select{
		Label: "Attach to",
		Items: labels,
		Size:  min(len(labels), 10),
	}
	idx, _, err := selector.Run()
	if err != nil {
		// Ctrl-C / Escape: exit cleanly with no error — conventional CLI behavior.
		if errors.Is(err, promptui.ErrInterrupt) {
			return nil
		}
		return fmt.Errorf("picker: %w", err)
	}
	return attachTmuxSession(res, choices[idx].session, opts)
}

// localAttachChoices combines configured processes (always visible, even when
// not currently running — attaching a stopped process surfaces the
// supervisor's failure message) with any live ephemeral agents the daemon
// reports. Names that collide between the two lists get a " (agent)" suffix
// on the agent entry so the picker is unambiguous.
func localAttachChoices(cfg *config.Config) []attachChoice {
	seen := make(map[string]struct{})
	out := make([]attachChoice, 0, len(cfg.Processes))

	procNames := make([]string, 0, len(cfg.Processes))
	for name := range cfg.Processes {
		procNames = append(procNames, name)
	}
	sort.Strings(procNames)
	for _, name := range procNames {
		out = append(out, attachChoice{
			label:   fmt.Sprintf("process  %s", name),
			session: processSessionName(name),
		})
		seen[name] = struct{}{}
	}

	// Daemon may be down (tests, fresh install) — absence of a daemon is
	// fine; just skip the agent list.
	records, err := daemon.AgentList(cfg.HomePath)
	if err == nil {
		agentRecords := append([]agent.Record(nil), records...)
		sort.Slice(agentRecords, func(i, j int) bool { return agentRecords[i].Name < agentRecords[j].Name })
		for _, rec := range agentRecords {
			label := fmt.Sprintf("agent    %s", rec.Name)
			if _, dup := seen[rec.Name]; dup {
				label = fmt.Sprintf("agent    %s (agent)", rec.Name)
			}
			out = append(out, attachChoice{
				label:   label,
				session: agent.SessionName(rec.Name),
			})
		}
	}
	return out
}

// remoteAttachChoices enumerates Leo-owned sessions on a remote host by
// running `tmux -L leo list-sessions -F '#{session_name}'` via the configured
// SSH transport. Sessions are filtered to the leo- prefix so unrelated state
// on the remote server doesn't bleed into the picker. Returns an empty slice
// (not an error) when tmux reports no sessions.
func remoteAttachChoices(res config.HostResolution) ([]attachChoice, error) {
	sshArgs := append([]string{res.Host.SSH}, res.Host.SSHArgs...)
	sshArgs = append(sshArgs, res.Host.RemoteTmuxPath())
	sshArgs = append(sshArgs, tmux.Args("list-sessions", "-F", "#{session_name}")...)

	cmd := agentExecCommand("ssh", sshArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// `tmux list-sessions` exits 1 when no server is running. Treat
		// that as "nothing attachable" rather than a hard error — the
		// remote may simply not have Leo's daemon up yet.
		stderrMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(stderrMsg, "no server running") || strings.Contains(stderrMsg, "no current session") {
			return nil, nil
		}
		if stderrMsg != "" {
			return nil, fmt.Errorf("listing remote sessions: %w: %s", err, stderrMsg)
		}
		return nil, fmt.Errorf("listing remote sessions: %w", err)
	}

	var choices []attachChoice
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, "leo-") {
			continue
		}
		choices = append(choices, attachChoice{
			label:   fmt.Sprintf("remote   %s", name),
			session: name,
		})
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].label < choices[j].label })
	return choices, nil
}

// stdinIsTerminal reports whether os.Stdin is connected to an interactive
// TTY. Indirected as a var so tests can simulate a pipe or terminal without
// touching real file descriptors.
var stdinIsTerminal = defaultStdinIsTerminal

func defaultStdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

