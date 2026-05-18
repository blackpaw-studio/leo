package run

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestWrapPromptWithMarkerAndFooter(t *testing.T) {
	out := wrapPromptForPersistent("abcdef0123456789abcdef0123456789", "hello", []string{"plugin:slack@official", "plugin:tg@official"})
	if !strings.Contains(out, "<!-- leo:invocation=abcdef0123456789abcdef0123456789 -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("missing body")
	}
	if !strings.Contains(out, "plugin:slack@official, plugin:tg@official") {
		t.Fatalf("missing channel footer")
	}
}

func TestWrapPromptOmitsFooterWhenNoChannels(t *testing.T) {
	out := wrapPromptForPersistent("abcdef0123456789abcdef0123456789", "hello", nil)
	if strings.Contains(out, "deliver your final reply") {
		t.Fatalf("expected no delivery footer when channels empty:\n%s", out)
	}
	if !strings.Contains(out, "<!-- leo:invocation=") {
		t.Fatalf("marker should still be present")
	}
}

func TestRunPersistentDispatchSelected(t *testing.T) {
	called := false
	orig := persistentImpl
	defer func() { persistentImpl = orig }()
	persistentImpl = func(cfg *config.Config, taskName string) error {
		called = true
		return nil
	}
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"t1": {Runtime: "persistent", PromptFile: "_", Workspace: "/tmp"},
		},
	}
	_ = Run(cfg, "t1", nil)
	if !called {
		t.Fatalf("expected runPersistent dispatch")
	}
}

func TestNewInvocationID16IsHex32(t *testing.T) {
	for i := 0; i < 5; i++ {
		id := newInvocationID16()
		if len(id) != 32 {
			t.Fatalf("expected 32 hex chars, got %d (%q)", len(id), id)
		}
		for _, ch := range id {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				t.Fatalf("non-hex char in id: %q", id)
			}
		}
	}
}
