package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const followerLeoYAML = "client:\n  default_host: localhost\n  hosts:\n    lead: {ssh: evan@10.0.2.10}\nfollow:\n  host: lead\n"

// loadFollowerWithLeadRules loads a follower whose lead exports one rule
// (name is leo-x → teal); local is appended to its leo.yaml.
func loadFollowerWithLeadRules(t *testing.T, local string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	export := `{"schema":2,"kind":"leo.follow.export","leo_version":"v0.80.0","home_id":"ab","revision":"sha256:r",` +
		`"templates":{},"environments":null,"delegation":null,"defaults":{},` +
		`"appearance_rules":[{"when":{"field":"name","op":"is","value":"leo-x"},"color":"teal"}]}`
	parsed, err := config.ParseFollowExport([]byte(export))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteFollowCache(filepath.Join(dir, "state"), config.FollowCache{
		Host: "lead", PulledAt: time.Now(), LeadRevision: parsed.Revision, Digest: parsed.Digest(), Export: json.RawMessage(export),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(path, []byte(followerLeoYAML+local), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAgentAppearanceUsesLeadRuleOnFollower(t *testing.T) {
	cfg := loadFollowerWithLeadRules(t, "")
	got := AgentAppearance(cfg, "leo-x", "", nil, nil)
	if got == nil || got.Color != "teal" || got.ColorSource != "rule:0" {
		t.Fatalf("appearance = %+v, want teal from the lead's rule:0", got)
	}
}

func TestAgentAppearanceLocalRulesReplaceLeadOnes(t *testing.T) {
	cfg := loadFollowerWithLeadRules(t, "appearance_rules: []\n")
	if got := AgentAppearance(cfg, "leo-x", "", nil, nil); got != nil {
		t.Fatalf("appearance = %+v, want none (the empty local list overrides the lead's)", got)
	}
}
