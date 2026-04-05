package env

import (
	"os"
	"testing"
)

func TestCaptureIncludesSetVars(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-123")
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot:token")

	result := Capture()

	if result["ANTHROPIC_API_KEY"] != "test-key-123" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", result["ANTHROPIC_API_KEY"], "test-key-123")
	}
	if result["TELEGRAM_BOT_TOKEN"] != "bot:token" {
		t.Errorf("TELEGRAM_BOT_TOKEN = %q, want %q", result["TELEGRAM_BOT_TOKEN"], "bot:token")
	}
}

func TestCaptureExcludesUnsetVars(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	os.Unsetenv("ANTHROPIC_API_KEY")

	result := Capture()

	if _, ok := result["ANTHROPIC_API_KEY"]; ok {
		t.Error("unset ANTHROPIC_API_KEY should not be in result")
	}
}

func TestCaptureIncludesPath(t *testing.T) {
	// PATH should always be set in a test environment
	result := Capture()

	if _, ok := result["PATH"]; !ok {
		t.Error("PATH should be present")
	}
}

func TestCaptureOnlyKnownKeys(t *testing.T) {
	t.Setenv("SOME_RANDOM_VAR", "value")

	result := Capture()

	if _, ok := result["SOME_RANDOM_VAR"]; ok {
		t.Error("unknown env vars should not be captured")
	}

	knownKeys := map[string]bool{
		"ANTHROPIC_API_KEY":      true,
		"CLAUDE_CODE_ENTRYPOINT": true,
		"HOME":                   true,
		"PATH":                   true,
		"SHELL":                  true,
		"USER":                   true,
		"TELEGRAM_BOT_TOKEN":     true,
	}

	for key := range result {
		if !knownKeys[key] {
			t.Errorf("unexpected key %q in result", key)
		}
	}
}
