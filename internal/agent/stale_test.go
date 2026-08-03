package agent

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// staleFixture builds a manager over one running agent whose record is seeded
// by seed, against a config with a single "coding" template. seed receives the
// wiring a fresh spawn would produce right now (curArgs/curEnv), so a case can
// store an already-current record — or deliberately drift one field — without
// hardcoding today's flag set. Everything shares one home, since the leo MCP
// config path is derived from it and would otherwise differ between the stored
// and re-resolved args.
func staleFixture(t *testing.T, tmpl config.TemplateConfig, seed func(r *agentstore.Record, curArgs []string, curEnv map[string]string)) *Manager {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Templates: map[string]config.TemplateConfig{"coding": tmpl},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	curArgs, curEnv := BuildTemplateArgs(cfg, tmpl, "leo-x", "/w", "", "tok")
	rec := agentstore.Record{
		Name:      "leo-x",
		Template:  "coding",
		Workspace: "/w",
		SessionID: "sid",
	}
	seed(&rec, curArgs, curEnv)
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatalf("seeding record: %v", err)
	}
	return New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
}

// TestStaleAgentsReportsEnvDrift is the case that motivated the feature: an
// upgrade adds a harness env var, and the running agent is still missing it.
func TestStaleAgentsReportsEnvDrift(t *testing.T) {
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, curArgs []string, _ map[string]string) {
		r.ClaudeArgs = curArgs
		// Stored env predates MCP_TOOL_TIMEOUT.
		r.Env = map[string]string{"LEGACY_KEY": "v"}
	})

	stale := m.StaleAgents()
	if len(stale) != 1 {
		t.Fatalf("StaleAgents() = %+v, want exactly one drifted agent", stale)
	}
	got := stale[0]
	if got.Name != "leo-x" {
		t.Errorf("Name = %q, want leo-x", got.Name)
	}
	if !slices.Contains(got.EnvAdded, "MCP_TOOL_TIMEOUT") {
		t.Errorf("EnvAdded = %v, want it to contain MCP_TOOL_TIMEOUT", got.EnvAdded)
	}
	if len(got.ArgsChanged) != 0 {
		t.Errorf("expected no args drift, got %v", got.ArgsChanged)
	}
}

// TestStaleAgentsNeverLeaksEnvValues guards the credential boundary: the drift
// report travels over the daemon API, and rec.Env holds live secrets.
func TestStaleAgentsNeverLeaksEnvValues(t *testing.T) {
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, _ []string, _ map[string]string) {
		r.ClaudeArgs = []string{"--model", "sonnet"}
		r.Env = map[string]string{"SECRET_TOKEN": "hunter2-do-not-leak"}
	})

	stale := m.StaleAgents()
	if len(stale) == 0 {
		t.Fatal("expected drift (model changed), got none")
	}
	blob := strings.Join(append(append([]string{},
		stale[0].EnvAdded...), append(append([]string{},
		stale[0].EnvChanged...), stale[0].EnvRemoved...)...), " ")
	if strings.Contains(blob, "hunter2-do-not-leak") {
		t.Fatalf("env VALUE leaked into the drift report: %v", stale[0])
	}
}

// TestStaleAgentsReportsArgsDrift covers a config edit (model change) rather
// than a binary upgrade.
func TestStaleAgentsReportsArgsDrift(t *testing.T) {
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, _ []string, curEnv map[string]string) {
		r.ClaudeArgs = []string{"--model", "sonnet"}
		r.Env = curEnv
	})

	stale := m.StaleAgents()
	if len(stale) != 1 {
		t.Fatalf("StaleAgents() = %+v, want one drifted agent", stale)
	}
	if !slices.Contains(stale[0].ArgsChanged, "--model sonnet -> opus") {
		t.Errorf("ArgsChanged = %v, want the model transition", stale[0].ArgsChanged)
	}
}

// TestStaleAgentsIgnoresSessionTokens is the false-positive guard: --resume /
// --session-id differ by construction and must not read as drift, or every
// agent would be reported on every update forever.
func TestStaleAgentsIgnoresSessionTokens(t *testing.T) {
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, curArgs []string, curEnv map[string]string) {
		r.ClaudeArgs = append(slices.Clone(curArgs), "--session-id", "sid")
		r.Env = curEnv
	})

	if stale := m.StaleAgents(); len(stale) != 0 {
		t.Fatalf("session tokens read as drift: %+v", stale)
	}
}

// TestStaleAgentsQuietWhenCurrent pins the happy path: nothing to report means
// `leo update` prints nothing at all.
func TestStaleAgentsQuietWhenCurrent(t *testing.T) {
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, curArgs []string, curEnv map[string]string) {
		r.ClaudeArgs = curArgs
		r.Env = curEnv
	})

	if stale := m.StaleAgents(); len(stale) != 0 {
		t.Fatalf("StaleAgents() = %+v, want none", stale)
	}
}

// TestStaleAgentsSkipsUnresolvableRecords covers the records restart itself
// refuses to re-resolve: a deleted template and a changed harness both fall
// back to stored args, so offering a restart would be a lie.
func TestStaleAgentsSkipsUnresolvableRecords(t *testing.T) {
	tests := []struct {
		name string
		seed func(*agentstore.Record)
	}{
		{"template deleted", func(r *agentstore.Record) { r.Template = "gone" }},
		{"no template (ad-hoc agent)", func(r *agentstore.Record) { r.Template = "" }},
		{"harness changed", func(r *agentstore.Record) { r.Harness = "codex" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, _ []string, _ map[string]string) {
				r.ClaudeArgs = []string{"--model", "ancient"}
				r.Env = map[string]string{"OLD": "1"}
				tt.seed(r)
			})
			if stale := m.StaleAgents(); len(stale) != 0 {
				t.Fatalf("StaleAgents() = %+v, want none (not re-resolvable)", stale)
			}
		})
	}
}

// TestStaleAgentsSkipsNotRunning: restart only acts on running agents, so a
// suspended one must not be offered. Post-fix it self-heals on resume anyway.
func TestStaleAgentsSkipsNotRunning(t *testing.T) {
	home := t.TempDir()
	tmpl := config.TemplateConfig{Model: "opus"}
	cfg := &config.Config{HomePath: home, Templates: map[string]config.TemplateConfig{"coding": tmpl}}
	sup := &capturingSupervisor{agents: map[string]ProcessState{}} // nothing running
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Workspace: "/w",
		ClaudeArgs: []string{"--model", "sonnet"},
		Env:        map[string]string{"OLD": "1"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if stale := m.StaleAgents(); len(stale) != 0 {
		t.Fatalf("StaleAgents() = %+v, want none (agent not running)", stale)
	}
}

// TestStaleAgentsMatchesWhatRestartApplies is the anti-divergence pin from the
// spec: whatever the dry run reports must be what a real restart produces, or
// leo would re-prompt about the same agent after every update.
func TestStaleAgentsMatchesWhatRestartApplies(t *testing.T) {
	home := t.TempDir()
	tmpl := config.TemplateConfig{Model: "opus"}
	cfg := &config.Config{HomePath: home, Templates: map[string]config.TemplateConfig{"coding": tmpl}}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Workspace: "/w", SessionID: "sid",
		ClaudeArgs: []string{"--model", "sonnet"},
		Env:        map[string]string{"LEGACY_KEY": "v"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	before := m.StaleAgents()
	if len(before) != 1 {
		t.Fatalf("expected drift before restart, got %+v", before)
	}
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if after := m.StaleAgents(); len(after) != 0 {
		t.Fatalf("agent still reports drift after a real restart: %+v", after)
	}
	// And the env the restart actually applied carries the added key.
	for _, k := range before[0].EnvAdded {
		if _, ok := sup.spawnCall.Env[k]; !ok {
			t.Errorf("reported EnvAdded %q never reached the spawn: %v", k, sup.spawnCall.Env)
		}
	}
}

// TestStaleAgentReportShape keeps the JSON contract stable for the daemon
// endpoint and the CLI that renders it.
func TestStaleAgentReportShape(t *testing.T) {
	got := StaleAgent{Name: "a", EnvAdded: []string{"K"}}
	want := StaleAgent{Name: "a", EnvAdded: []string{"K"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("StaleAgent is not comparable by value")
	}
}

// TestStaleAgentsRedactsFreeFormArgValues is the argv half of the credential
// boundary. An agent's whole --append-system-prompt lands in ClaudeArgs, so a
// drift report must name the flag and say it changed rather than echoing the
// text — this report is served over the daemon API and printed to a terminal.
func TestStaleAgentsRedactsFreeFormArgValues(t *testing.T) {
	longPrompt := "SECRET-PROMPT-BODY " + strings.Repeat("x", 200)
	m := staleFixture(t, config.TemplateConfig{Model: "opus"}, func(r *agentstore.Record, curArgs []string, curEnv map[string]string) {
		r.ClaudeArgs = append(slices.Clone(curArgs), "--append-system-prompt", longPrompt)
		r.Env = curEnv
	})

	stale := m.StaleAgents()
	if len(stale) != 1 {
		t.Fatalf("StaleAgents() = %+v, want one drifted agent", stale)
	}
	joined := strings.Join(stale[0].ArgsChanged, " | ")
	if strings.Contains(joined, "SECRET-PROMPT-BODY") || strings.Contains(joined, longPrompt) {
		t.Fatalf("free-form arg value leaked into the report: %q", joined)
	}
	if !strings.Contains(joined, "--append-system-prompt") {
		t.Errorf("expected the flag to be named: %q", joined)
	}
}

// TestSummarizeArgsDelta covers the delta shapes directly, including the
// multi-line value case a system prompt hits.
func TestSummarizeArgsDelta(t *testing.T) {
	tests := []struct {
		name          string
		before, after []string
		want          []string
	}{
		{"value change", []string{"--model", "sonnet"}, []string{"--model", "opus"}, []string{"--model sonnet -> opus"}},
		{"flag added", []string{}, []string{"--remote-control"}, []string{"+--remote-control"}},
		{"flag removed", []string{"--remote-control"}, []string{}, []string{"---remote-control"}},
		{"flag added with value", []string{}, []string{"--model", "opus"}, []string{"+--model opus"}},
		{
			"long value elided",
			[]string{"--append-system-prompt", strings.Repeat("a", 100)},
			[]string{"--append-system-prompt", strings.Repeat("b", 100)},
			[]string{"--append-system-prompt changed"},
		},
		{
			"multi-line value elided",
			[]string{"--append-system-prompt", "one\ntwo"},
			[]string{"--append-system-prompt", "three\nfour"},
			[]string{"--append-system-prompt changed"},
		},
		{
			"long value added is not echoed",
			[]string{},
			[]string{"--append-system-prompt", strings.Repeat("a", 100)},
			[]string{"+--append-system-prompt (set)"},
		},
		{
			"positional count only, never content",
			[]string{"--model", "opus"},
			[]string{"--model", "opus", "some secret opening prompt"},
			[]string{"positional args 0 -> 1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeArgsDelta(tt.before, tt.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("summarizeArgsDelta() = %v, want %v", got, tt.want)
			}
		})
	}
}
