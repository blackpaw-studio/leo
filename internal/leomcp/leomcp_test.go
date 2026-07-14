package leomcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestEnsureConfigWritesFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir}

	path, err := EnsureConfig(cfg)
	if err != nil {
		t.Fatalf("EnsureConfig: %v", err)
	}
	if !strings.HasSuffix(path, "/state/leo-mcp.json") {
		t.Errorf("path = %q, want suffix /state/leo-mcp.json", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	leo, ok := parsed.MCPServers["leo"]
	if !ok {
		t.Fatalf("missing leo entry in %s", raw)
	}
	if leo.Command != "leo" || len(leo.Args) != 1 || leo.Args[0] != "mcp-server" {
		t.Errorf("unexpected leo entry: %+v", leo)
	}
}

func TestEnsureConfigIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir}

	path, err := EnsureConfig(cfg)
	if err != nil {
		t.Fatalf("first EnsureConfig: %v", err)
	}
	stat1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if _, err := EnsureConfig(cfg); err != nil {
		t.Fatalf("second EnsureConfig: %v", err)
	}
	stat2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file rewritten on second call (mtime changed: %v -> %v)", stat1.ModTime(), stat2.ModTime())
	}
}

func TestAppendArgIncludedWhenWebDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir}
	cfg.Web.Enabled = false

	args := AppendArg([]string{"--model", "sonnet"}, cfg)
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mcp-config" && strings.HasSuffix(args[i+1], "leo-mcp.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --mcp-config <leo-mcp.json> in args even when web is disabled; got %v", args)
	}

	// The MCP server is always wired in now (it self-selects local-only
	// mode from its env), so the file should be written.
	if _, err := os.Stat(ConfigPath(cfg)); err != nil {
		t.Errorf("leo MCP file should exist even when web is disabled: %v", err)
	}
}

func TestAppendArgIncludedWhenWebEnabled(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir}
	cfg.Web.Enabled = true

	args := AppendArg([]string{"--model", "sonnet"}, cfg)
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mcp-config" && strings.HasSuffix(args[i+1], "leo-mcp.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --mcp-config <leo-mcp.json> in args; got %v", args)
	}
}

func TestAppendArgSkippedWhenHomePathEmpty(t *testing.T) {
	cfg := &config.Config{}
	cfg.Web.Enabled = true

	args := AppendArg([]string{"--model", "sonnet"}, cfg)
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mcp-config" {
			t.Fatalf("expected no --mcp-config when HomePath is empty; got %v", args)
		}
	}
	if len(args) != 2 {
		t.Errorf("expected args unchanged when HomePath is empty; got %v", args)
	}
}

func TestMergeSystemPrompt(t *testing.T) {
	enabled := &config.Config{Web: config.WebConfig{Enabled: true}}
	disabled := &config.Config{Web: config.WebConfig{Enabled: false}}

	tests := []struct {
		name      string
		cfg       *config.Config
		user      string
		wantHas   []string // substrings that must appear
		wantEmpty bool
	}{
		{"disabled+no user", disabled, "", []string{"leo_skill"}, false},
		{"disabled keeps user only", disabled, "be terse", []string{"be terse", "leo_skill"}, false},
		{"enabled adds awareness", enabled, "", []string{"leo_send_message"}, false},
		{"enabled merges both", enabled, "be terse", []string{"leo_send_message", "be terse"}, false},
		{"nil cfg keeps user", nil, "be terse", []string{"be terse"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeSystemPrompt(tt.cfg, tt.user)
			if tt.wantEmpty && got != "" {
				t.Fatalf("want empty, got %q", got)
			}
			for _, sub := range tt.wantHas {
				if !strings.Contains(got, sub) {
					t.Errorf("result missing %q; got %q", sub, got)
				}
			}
			// When enabled, the awareness text precedes the user text.
			if tt.cfg != nil && tt.cfg.Web.Enabled && tt.user != "" {
				if strings.Index(got, "leo_send_message") > strings.Index(got, tt.user) {
					t.Errorf("awareness text should come before user text; got %q", got)
				}
			}
		})
	}
}

func TestLeoNudge(t *testing.T) {
	enabled := &config.Config{Web: config.WebConfig{Enabled: true}}
	disabled := &config.Config{Web: config.WebConfig{Enabled: false}}

	tests := []struct {
		name      string
		cfg       *config.Config
		wantEmpty bool
		wantHas   []string
	}{
		{"nil cfg", nil, true, nil},
		{"web disabled", disabled, false, []string{"leo_skill"}},
		{"web enabled", enabled, false, []string{"leo_send_message", "leo_skill"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LeoNudge(tt.cfg)
			if tt.wantEmpty && got != "" {
				t.Fatalf("want empty, got %q", got)
			}
			if !tt.wantEmpty && got == "" {
				t.Fatalf("want non-empty nudge, got empty")
			}
			for _, sub := range tt.wantHas {
				if !strings.Contains(got, sub) {
					t.Errorf("result missing %q; got %q", sub, got)
				}
			}
			if tt.name == "web disabled" && strings.Contains(got, "leo_send_message") {
				t.Errorf("web-disabled nudge should not mention leo_send_message; got %q", got)
			}
		})
	}
}
