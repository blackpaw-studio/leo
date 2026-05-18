package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLeoStopHookFromEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLeoStopHook(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stops := got["hooks"].(map[string]any)["Stop"].([]any)
	if len(stops) != 1 {
		t.Fatalf("expected 1 Stop hook, got %d", len(stops))
	}
	entry := stops[0].(map[string]any)
	if entry["_leo_managed"] != "task-report" || entry["command"] != "leo internal task-report" {
		t.Fatalf("hook entry wrong: %#v", entry)
	}
}

func TestEnsureLeoStopHookPreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"hooks":{"Stop":[{"command":"my-user-hook"}],"PreToolUse":[{"matcher":"Write","command":"fmt"}]}}`
	if err := os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := EnsureLeoStopHook(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(cdir, "settings.local.json"))
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	stops := got["hooks"].(map[string]any)["Stop"].([]any)
	if len(stops) != 2 {
		t.Fatalf("expected user hook + leo hook, got %d", len(stops))
	}
	pre := got["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse hooks were dropped")
	}
}

func TestEnsureLeoStopHookIdempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := EnsureLeoStopHook(dir); err != nil {
			t.Fatalf("ensure iter %d: %v", i, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	stops := got["hooks"].(map[string]any)["Stop"].([]any)
	leoCount := 0
	for _, s := range stops {
		if s.(map[string]any)["_leo_managed"] == "task-report" {
			leoCount++
		}
	}
	if leoCount != 1 {
		t.Fatalf("expected exactly 1 leo-managed entry after repeated ensure, got %d", leoCount)
	}
}

func TestEnsureLeoStopHookRefusesMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, ".claude")
	_ = os.MkdirAll(cdir, 0o755)
	_ = os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte("{not json"), 0o644)
	if err := EnsureLeoStopHook(dir); err == nil {
		t.Fatalf("expected error on malformed json, got nil")
	}
}
