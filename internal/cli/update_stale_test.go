package cli

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func twoStale() []agent.StaleAgent {
	return []agent.StaleAgent{
		{Name: "chronicle", EnvAdded: []string{"MCP_TOOL_TIMEOUT"}},
		{Name: "assistant", ArgsChanged: []string{"--model sonnet -> opus"}},
	}
}

// TestPromptStaleAgentsQuietWhenNoDrift: the happy path after most updates is
// that nothing changed for any agent, and leo must not add noise for it.
func TestPromptStaleAgentsQuietWhenNoDrift(t *testing.T) {
	var out bytes.Buffer
	var restarted []string

	err := promptStaleAgentRestart(nil, 0, &out, bufio.NewReader(strings.NewReader("")), true,
		func(name string) error { restarted = append(restarted, name); return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output with no drift, got %q", out.String())
	}
	if len(restarted) != 0 {
		t.Errorf("expected no restarts, got %v", restarted)
	}
}

// TestPromptStaleAgentsRestartsOnlyDrifted: accepting the prompt bounces the
// reported agents and nothing else — not a blanket restart-all.
func TestPromptStaleAgentsRestartsOnlyDrifted(t *testing.T) {
	var out bytes.Buffer
	var restarted []string

	// Empty line accepts the [Y/n] default.
	err := promptStaleAgentRestart(twoStale(), 9, &out, bufio.NewReader(strings.NewReader("\n")), true,
		func(name string) error { restarted = append(restarted, name); return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(restarted) != 2 {
		t.Fatalf("restarted = %v, want both drifted agents", restarted)
	}
	for _, want := range []string{"chronicle", "assistant"} {
		if !contains(restarted, want) {
			t.Errorf("agent %q was not restarted (got %v)", want, restarted)
		}
	}
}

// TestPromptStaleAgentsDeclined: answering no leaves every agent alone.
func TestPromptStaleAgentsDeclined(t *testing.T) {
	var out bytes.Buffer
	var restarted []string

	err := promptStaleAgentRestart(twoStale(), 9, &out, bufio.NewReader(strings.NewReader("n\n")), true,
		func(name string) error { restarted = append(restarted, name); return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(restarted) != 0 {
		t.Fatalf("declining still restarted %v", restarted)
	}
	if !strings.Contains(out.String(), "chronicle") {
		t.Errorf("expected the drifted agents to still be listed, got %q", out.String())
	}
}

// TestPromptStaleAgentsNonInteractive: with nobody to answer, list the agents
// and name the remedy, but never bounce anything unattended. Mirrors how the
// daemon-restart prompt already degrades.
func TestPromptStaleAgentsNonInteractive(t *testing.T) {
	var out bytes.Buffer
	var restarted []string

	err := promptStaleAgentRestart(twoStale(), 9, &out, nil, false,
		func(name string) error { restarted = append(restarted, name); return nil })

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(restarted) != 0 {
		t.Fatalf("non-interactive run restarted %v, want none", restarted)
	}
	got := out.String()
	if !strings.Contains(got, "chronicle") || !strings.Contains(got, "leo agent restart") {
		t.Errorf("expected the agent list and the remedy command, got %q", got)
	}
}

// TestPromptStaleAgentsReportsPerAgentFailure: one failing restart is reported
// and the rest still run, matching RestartAll's batch semantics.
func TestPromptStaleAgentsReportsPerAgentFailure(t *testing.T) {
	var out bytes.Buffer
	var restarted []string

	err := promptStaleAgentRestart(twoStale(), 9, &out, bufio.NewReader(strings.NewReader("y\n")), true,
		func(name string) error {
			if name == "chronicle" {
				return errors.New("boom")
			}
			restarted = append(restarted, name)
			return nil
		})

	if err != nil {
		t.Fatalf("a per-agent failure must not fail the update: %v", err)
	}
	if !contains(restarted, "assistant") {
		t.Errorf("the batch stopped early: %v", restarted)
	}
	if !strings.Contains(out.String(), "boom") {
		t.Errorf("failure not surfaced: %q", out.String())
	}
}

// TestPromptStaleAgentsRendersDrift checks the operator can tell WHY each agent
// is listed — an env key name or an args change — without any env values.
func TestPromptStaleAgentsRendersDrift(t *testing.T) {
	var out bytes.Buffer
	_ = promptStaleAgentRestart(twoStale(), 9, &out, bufio.NewReader(strings.NewReader("n\n")), true,
		func(string) error { return nil })

	got := out.String()
	if !strings.Contains(got, "MCP_TOOL_TIMEOUT") {
		t.Errorf("env drift key not rendered: %q", got)
	}
	if !strings.Contains(got, "opus") {
		t.Errorf("args drift not rendered: %q", got)
	}
	if !strings.Contains(got, "2 of 9 running agents") {
		t.Errorf("expected an \"N of M\" summary line: %q", got)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
