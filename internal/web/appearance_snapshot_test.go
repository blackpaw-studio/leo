package web

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// appearanceSnapshotConfig gives the "ops" template the homelab environment,
// so the rules can test it.
func appearanceSnapshotConfig() *config.Config {
	return &config.Config{
		Defaults:     config.DefaultsConfig{Model: "sonnet"},
		Environments: map[string]config.EnvMap{"homelab": config.LiteralEnv(map[string]string{"X": "1"})},
		Templates: map[string]config.TemplateConfig{
			"ops":       {Model: "opus", Environments: []string{"homelab"}},
			"assistant": {Model: "sonnet"},
		},
		AppearanceRules: config.AppearanceRuleList{
			{When: config.AppearanceCondition{Field: "environments", Op: "contains", Value: "homelab"}, Color: "teal", Emoji: "🏠"},
			{When: config.AppearanceCondition{Field: "name", Op: "glob", Value: "alfred-*"}, Color: "pink"},
		},
	}
}

// assertAgentWire compares one projected agent with the observe package's
// golden wire fixture.
func assertAgentWire(t *testing.T, fixture string, got any) {
	t.Helper()
	want, err := os.ReadFile("../observe/testdata/wire/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wantDoc, gotDoc any
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotJSON, &gotDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("%s mismatch\n--- want\n%s\n--- got\n%s", fixture, want, gotJSON)
	}
}

func TestSnapshotAppearanceSources(t *testing.T) {
	records := []agent.Record{
		{
			Name: "olympus", Template: "ops", Workspace: "/home/leo/workspace", Status: "running",
			StartedAt:        time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC),
			ManualAppearance: &agentstore.Appearance{Emoji: "🏛️"},
		},
		{
			Name: "alfred-itunes-dj", Template: "assistant", Workspace: "/home/leo/workspace", Status: "stopped",
			WakeOnMessage: true, Restarts: 2,
			StartedAt: time.Date(2026, 10, 27, 13, 30, 0, 0, time.UTC),
		},
	}
	activity := staticActivityProvider{"olympus": {Activity: observe.ActivityIdle}}
	agents := ProjectAgents(records, nil, AgentSources{Activity: activity}, appearanceSnapshotConfig())
	if len(agents) != 2 {
		t.Fatalf("projected %d agents, want 2", len(agents))
	}
	// Sorted by name: alfred-itunes-dj, olympus.
	assertAgentWire(t, "appearance-agent-partial.json", agents[0])
	assertAgentWire(t, "appearance-agent.json", agents[1])
}

func TestSnapshotAppearanceOmittedWhenUnset(t *testing.T) {
	cfg := appearanceSnapshotConfig()
	agents := ProjectAgents([]agent.Record{{Name: "plain", Template: "assistant"}}, nil, AgentSources{}, cfg)
	b, err := json.Marshal(agents[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"appearance"`) {
		t.Fatalf("agent with no appearance carries the key: %s", b)
	}
	// Without config, a manual value still resolves.
	agents = ProjectAgents([]agent.Record{{Name: "plain", ManualAppearance: &agentstore.Appearance{Color: "red"}}}, nil, AgentSources{}, nil)
	if a := agents[0].Appearance; a == nil || a.Color != "red" || a.ColorSource != "manual" {
		t.Fatalf("nil-config appearance = %+v, want manual red", a)
	}
}
