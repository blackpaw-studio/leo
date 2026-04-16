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

func TestAppendArgSkippedWhenWebDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir}
	cfg.Web.Enabled = false

	args := AppendArg([]string{"--model", "sonnet"}, cfg)
	for i, a := range args {
		if a == "--mcp-config" {
			t.Errorf("--mcp-config unexpectedly added at %d: %v", i, args)
		}
	}

	// File should not have been written either.
	if _, err := os.Stat(ConfigPath(cfg)); !os.IsNotExist(err) {
		t.Errorf("leo MCP file should not exist when web is disabled (err=%v)", err)
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
