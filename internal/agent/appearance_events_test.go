package agent

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// appearanceEvents returns the agent_appearance_changed payloads published so
// far, in order.
func appearanceEvents(pub *recordingObservePublisher) []*observe.AgentAppearanceChangedPayload {
	var out []*observe.AgentAppearanceChangedPayload
	for _, ev := range pub.events {
		if p, ok := ev.Payload.(*observe.AgentAppearanceChangedPayload); ok && ev.Type == observe.EventAgentAppearanceChanged {
			out = append(out, p)
		}
	}
	return out
}

func lastAppearanceEvent(t *testing.T, pub *recordingObservePublisher, name string) *observe.AgentAppearanceChangedPayload {
	t.Helper()
	evs := appearanceEvents(pub)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Agent == name {
			return evs[i]
		}
	}
	t.Fatalf("no agent_appearance_changed for %q in %+v", name, pub.events)
	return nil
}

func strRef(s string) *string { return &s }

func colorRule(field, op, value, color string) config.AppearanceRule {
	return config.AppearanceRule{When: config.AppearanceCondition{Field: field, Op: op, Value: value}, Color: color}
}

func TestSetEnvironmentsRepublishesAppearance(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	cfg.AppearanceRules = config.AppearanceRuleList{colorRule("environments", "contains", "acct-b", "blue")}
	_, sup := savedLayeredAgent(t, cfg, nil)
	pub := &recordingObservePublisher{}
	m := envManager(cfg, sup)
	m.SetPublisher(pub)

	if _, err := m.SetEnvironments("leo-x", []string{"base", "acct-b"}); err != nil {
		t.Fatal(err)
	}
	got := lastAppearanceEvent(t, pub, "leo-x").Appearance
	if got == nil || got.Color != "blue" || got.ColorSource != "rule:0" {
		t.Fatalf("appearance after moving to acct-b = %+v, want blue from rule:0", got)
	}

	if _, err := m.SetEnvironments("leo-x", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastAppearanceEvent(t, pub, "leo-x").Appearance; got != nil {
		t.Fatalf("appearance after leaving acct-b = %+v, want null", got)
	}
}

func TestRepublishAppearancePublishesEveryAgent(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:        home,
		Templates:       map[string]config.TemplateConfig{"coding": {}},
		AppearanceRules: config.AppearanceRuleList{colorRule("template", "is", "coding", "teal")},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "coder", Template: "coding", Stopped: true})
	_ = agentstore.Save(home, agentstore.Record{Name: "plain", Stopped: true})
	if _, err := agentstore.SetAppearance(home, "plain", agentstore.AppearancePatch{Emoji: strRef("🎩")}); err != nil {
		t.Fatal(err)
	}
	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, &capturingSupervisor{}, "", "tok")
	m.SetPublisher(pub)

	m.RepublishAppearance()

	if n := len(appearanceEvents(pub)); n != 2 {
		t.Fatalf("published %d appearance events, want one per agent (2)", n)
	}
	if got := lastAppearanceEvent(t, pub, "coder").Appearance; got == nil || got.Color != "teal" {
		t.Fatalf("coder = %+v, want teal", got)
	}
	if got := lastAppearanceEvent(t, pub, "plain").Appearance; got == nil || got.Emoji != "🎩" || got.EmojiSource != "manual" {
		t.Fatalf("plain = %+v, want manual 🎩", got)
	}
}

func TestSwitchTemplateRepublishesAppearance(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	cfg.AppearanceRules = config.AppearanceRuleList{colorRule("harness", "is", "codex", "gray")}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w", Stopped: true,
	})
	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, &capturingSupervisor{}, "", "tok")
	m.SetPublisher(pub)

	if _, err := m.SwitchTemplate("leo-x", "codex"); err != nil {
		t.Fatal(err)
	}
	if got := lastAppearanceEvent(t, pub, "leo-x").Appearance; got == nil || got.Color != "gray" {
		t.Fatalf("appearance after switching to codex = %+v, want gray", got)
	}
}

func TestRenameStoppedAgentSpawnedViewCarriesAppearance(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:        home,
		AppearanceRules: config.AppearanceRuleList{colorRule("name", "starts_with", "leo-re", "green")},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-stopped", Stopped: true, ClaudeArgs: []string{"--name", "leo-stopped"}})
	if _, err := agentstore.SetAppearance(home, "leo-stopped", agentstore.AppearancePatch{Emoji: strRef("🏛️")}); err != nil {
		t.Fatal(err)
	}
	pub := &recordingObservePublisher{}
	m := New(func() (*config.Config, error) { return cfg, nil }, &fakeSupervisor{ephemeral: map[string]ProcessState{}}, "", "")
	m.SetPublisher(pub)

	if _, err := m.Rename("leo-stopped", "leo-revived"); err != nil {
		t.Fatal(err)
	}
	var spawned *observe.AgentSpawnedPayload
	for _, ev := range pub.events {
		if p, ok := ev.Payload.(*observe.AgentSpawnedPayload); ok {
			spawned = p
		}
	}
	if spawned == nil {
		t.Fatalf("no agent_spawned in %+v", pub.events)
	}
	got := spawned.Agent.Appearance
	if got == nil || got.Emoji != "🏛️" || got.EmojiSource != "manual" || got.Color != "green" {
		t.Fatalf("spawned appearance = %+v, want the carried manual emoji plus green for the new name", got)
	}
}

func TestRenameLiveAgentPublishesAppearance(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:        home,
		AppearanceRules: config.AppearanceRuleList{colorRule("name", "is", "leo-renamed-agent", "orange")},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-old", ClaudeArgs: []string{"--name", "leo-old"}})
	pub := &recordingObservePublisher{}
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "")
	m.SetPublisher(pub)

	if _, err := m.Rename("leo-old", "renamed-agent"); err != nil {
		t.Fatal(err)
	}
	if got := lastAppearanceEvent(t, pub, "leo-renamed-agent").Appearance; got == nil || got.Color != "orange" {
		t.Fatalf("appearance after a live rename = %+v, want orange", got)
	}
}
