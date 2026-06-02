package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// stopHooks reads the Stop array from a settings.local.json under dir.
func stopHooks(t *testing.T, dir string) []any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	hooks, _ := got["hooks"].(map[string]any)
	stops, _ := hooks["Stop"].([]any)
	return stops
}

// assertLeoCommandRunnable verifies the leo entry is shaped the way Claude Code
// actually executes Stop hooks: a matcher group with a nested `hooks` array
// containing a {type:"command", command:...} object. A flat {command:...}
// entry would parse here as a missing command and the hook would never run.
func assertLeoCommandRunnable(t *testing.T, stops []any) {
	t.Helper()
	for _, s := range stops {
		group, ok := s.(map[string]any)
		if !ok || group["_leo_managed"] != leoStopHookLabel {
			continue
		}
		inner, ok := group["hooks"].([]any)
		if !ok || len(inner) == 0 {
			t.Fatalf("leo Stop entry has no nested hooks array: %#v", group)
		}
		for _, h := range inner {
			cmd, ok := h.(map[string]any)
			if ok && cmd["type"] == "command" && cmd["command"] == leoStopCommand {
				return // found a runnable command in Claude's schema
			}
		}
		t.Fatalf("leo Stop entry has no {type:command, command:%q}: %#v", leoStopCommand, group)
	}
	t.Fatalf("no leo-managed Stop entry found in %#v", stops)
}

func TestEnsureLeoStopHookFromEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureLeoStopHook(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	stops := stopHooks(t, dir)
	if len(stops) != 1 {
		t.Fatalf("expected 1 Stop hook, got %d", len(stops))
	}
	assertLeoCommandRunnable(t, stops)
}

// TestEnsureLeoStopHookMigratesFlatLegacyEntry covers upgrading from a prior
// leo version that wrote the (broken) flat {_leo_managed, command} shape: the
// installer must replace it with the nested, Claude-runnable form, not stack a
// second entry.
func TestEnsureLeoStopHookMigratesFlatLegacyEntry(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"hooks":{"Stop":[{"_leo_managed":"task-report","command":"leo internal task-report"}]}}`
	if err := os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if err := EnsureLeoStopHook(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	stops := stopHooks(t, dir)
	if len(stops) != 1 {
		t.Fatalf("expected legacy entry replaced by exactly 1, got %d: %#v", len(stops), stops)
	}
	assertLeoCommandRunnable(t, stops)
}

func TestEnsureLeoStopHookPreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	cdir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my-user-hook"}]}],"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"fmt"}]}]}}`
	if err := os.WriteFile(filepath.Join(cdir, "settings.local.json"), []byte(seed), 0o600); err != nil {
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
	assertLeoCommandRunnable(t, stops)
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
