package credentials

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const testHome = "/Users/tester"

func TestAccountsFromEnvironments(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvMap{
		"blackpaw": {"CLAUDE_CONFIG_DIR": config.Literal("~/.claude-blackpaw")},
		"work":     {"CLAUDE_CONFIG_DIR": config.Literal("${HOME}/.claude-work/"), "CODEX_HOME": config.Literal("/srv/codex-work")},
	}}

	got := Accounts(cfg, testHome)

	want := []Account{
		{Kind: KindClaudeCode, Dir: "/Users/tester/.claude", Label: ".claude"},
		{Kind: KindCodex, Dir: "/Users/tester/.codex", Label: ".codex"},
		{Kind: KindClaudeCode, Dir: "/Users/tester/.claude-blackpaw", Label: "blackpaw"},
		{Kind: KindClaudeCode, Dir: "/Users/tester/.claude-work", Label: "work"},
		{Kind: KindCodex, Dir: "/srv/codex-work", Label: "work"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Accounts() = %+v\nwant %+v", got, want)
	}
}

func TestAccountsNilConfigListsDefaults(t *testing.T) {
	got := Accounts(nil, testHome)
	if len(got) != 2 || got[0].Dir != "/Users/tester/.claude" || got[1].Dir != "/Users/tester/.codex" {
		t.Fatalf("Accounts(nil) = %+v, want the two defaults", got)
	}
}

func TestAccountsSkipsSecretRefsRelativeAndUnsetPaths(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvMap{
		"secret":   {"CLAUDE_CONFIG_DIR": config.Secret(secretenv.Ref{})},
		"relative": {"CLAUDE_CONFIG_DIR": config.Literal("some/dir")},
		"unset":    {"CLAUDE_CONFIG_DIR": config.Literal("$LEO_TEST_DEFINITELY_UNSET_VAR/x")},
		"empty":    {"CODEX_HOME": config.Literal("")},
	}}
	if got := Accounts(cfg, testHome); len(got) != 2 {
		t.Fatalf("Accounts() = %+v, want only the two defaults", got)
	}
}

func TestAccountsDedupsByKindAndDirPreferringEnvName(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvMap{
		"home-claude": {"CLAUDE_CONFIG_DIR": config.Literal("~/.claude")},
		"dup":         {"CLAUDE_CONFIG_DIR": config.Literal("/Users/tester/./.claude/../.claude")},
	}}
	got := Accounts(cfg, testHome)
	if len(got) != 2 {
		t.Fatalf("Accounts() = %+v, want 2 after dedup", got)
	}
	// Environments are visited in name order; the first to name a dir labels it.
	if got[0].Label != "dup" {
		t.Fatalf("default dir label = %q, want the first environment's name", got[0].Label)
	}
}
