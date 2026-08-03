package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
)

// maybeRestartStaleAgents runs after a successful daemon restart: it asks the
// (new) daemon which running agents would change if restarted, and offers to
// bounce exactly those.
//
// Ordering is load-bearing. The re-resolution happens inside the daemon, so
// asking one that is still running the previous binary would answer with the
// old binary's logic — hence this is only ever called once the daemon has been
// restarted. A failure to reach the daemon is reported and swallowed: the
// update itself succeeded, and a broken drift check must not fail it.
func maybeRestartStaleAgents(ctx context.Context, homePath string) {
	stale, err := daemon.AgentStale(ctx, homePath)
	if err != nil {
		// An older daemon has no /agents/stale route; there is nothing useful
		// to say to the operator about that, so stay quiet unless it's a real
		// failure worth seeing.
		warn.Printf("Could not check agents for pending changes: %v\n", err)
		return
	}
	// Total running count is context only ("3 of 9"), so a failure to fetch
	// it degrades the header rather than the feature.
	running := 0
	if recs, err := daemon.AgentList(ctx, homePath); err == nil {
		for _, r := range recs {
			if r.Status == "running" {
				running++
			}
		}
	}

	reader := bufio.NewReader(os.Stdin)
	if err := promptStaleAgentRestart(stale, running, os.Stdout, reader, prompt.IsInteractive(), func(name string) error {
		return daemon.AgentRestart(ctx, homePath, name)
	}); err != nil {
		warn.Printf("Restarting agents: %v\n", err)
	}
}

// promptStaleAgentRestart renders the drift report and, when the operator
// agrees, restarts exactly the listed agents via restart.
//
// Prints nothing at all when there is no drift — the common case after most
// updates. Without a terminal it lists the agents and names the remedy but
// bounces nothing: restarting an agent interrupts whatever turn it is running,
// which is not something to do unattended on an empty stdin read.
//
// A per-agent failure is reported and the batch continues, matching
// RestartAll's semantics. The returned error is reserved for a failure of the
// prompt itself; a failed restart never fails the update.
func promptStaleAgentRestart(stale []agent.StaleAgent, totalRunning int, out io.Writer, reader *bufio.Reader, interactive bool, restart func(name string) error) error {
	if len(stale) == 0 {
		return nil
	}

	fmt.Fprintf(out, "\n%s would pick up changes:\n", staleHeadcount(len(stale), totalRunning))
	for _, s := range stale {
		fmt.Fprintf(out, "  %-24s %s\n", s.Name, describeDrift(s))
	}

	if !interactive {
		fmt.Fprintf(out, "\nRestart them when ready with: leo agent restart --all\n")
		return nil
	}
	if !prompt.YesNo(reader, "\nRestart them now?", true) {
		fmt.Fprintf(out, "Skipped. Restart them later with: leo agent restart --all\n")
		return nil
	}

	for _, s := range stale {
		if err := restart(s.Name); err != nil {
			fmt.Fprintf(out, "  %-24s failed: %v\n", s.Name, err)
			continue
		}
		fmt.Fprintf(out, "  %-24s restarted\n", s.Name)
	}
	return nil
}

// staleHeadcount renders "3 of 9 running agents", degrading to a bare count
// when the running total could not be fetched or would read oddly.
func staleHeadcount(stale, totalRunning int) string {
	noun := "running agents"
	if totalRunning == 1 || (totalRunning <= 0 && stale == 1) {
		noun = "running agent"
	}
	if totalRunning > stale {
		return fmt.Sprintf("%d of %d %s", stale, totalRunning, noun)
	}
	return fmt.Sprintf("%d %s", stale, noun)
}

// describeDrift summarizes one agent's drift for the prompt: env key names
// (never values — StaleAgent carries none) and the argv delta.
func describeDrift(s agent.StaleAgent) string {
	var parts []string
	if keys := envDriftKeys(s); keys != "" {
		parts = append(parts, "env: "+keys)
	}
	if len(s.ArgsAfter) > 0 || len(s.ArgsBefore) > 0 {
		parts = append(parts, "args: "+argsDelta(s.ArgsBefore, s.ArgsAfter))
	}
	if len(parts) == 0 {
		return "config changed"
	}
	return strings.Join(parts, "  ")
}

func envDriftKeys(s agent.StaleAgent) string {
	var keys []string
	for _, k := range s.EnvAdded {
		keys = append(keys, "+"+k)
	}
	for _, k := range s.EnvChanged {
		keys = append(keys, "~"+k)
	}
	for _, k := range s.EnvRemoved {
		keys = append(keys, "-"+k)
	}
	return strings.Join(keys, " ")
}

// argsDelta renders only the tokens that actually differ, so a one-flag change
// doesn't print two full command lines.
func argsDelta(before, after []string) string {
	removed := tokenDiff(before, after)
	added := tokenDiff(after, before)
	switch {
	case len(removed) == 0 && len(added) == 0:
		return "reordered"
	case len(removed) == 0:
		return "+" + strings.Join(added, " ")
	case len(added) == 0:
		return "-" + strings.Join(removed, " ")
	default:
		return strings.Join(removed, " ") + " -> " + strings.Join(added, " ")
	}
}

// tokenDiff returns the tokens in a that are absent from b.
func tokenDiff(a, b []string) []string {
	inB := make(map[string]int, len(b))
	for _, t := range b {
		inB[t]++
	}
	var out []string
	for _, t := range a {
		if inB[t] > 0 {
			inB[t]--
			continue
		}
		out = append(out, t)
	}
	return out
}
