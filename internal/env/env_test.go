package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureIncludesSetVars(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-123")

	result := Capture()

	if result["ANTHROPIC_API_KEY"] != "test-key-123" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want %q", result["ANTHROPIC_API_KEY"], "test-key-123")
	}
}

func TestCaptureExcludesTelegramBotToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot:token")

	result := Capture()

	if _, ok := result["TELEGRAM_BOT_TOKEN"]; ok {
		t.Error("TELEGRAM_BOT_TOKEN is channel-plugin-owned; Leo must not capture it")
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

func TestCaptureLocalBinInPath(t *testing.T) {
	origHome := userHomeDirFn
	origStat := statFn
	defer func() {
		userHomeDirFn = origHome
		statFn = origStat
	}()

	home := t.TempDir()
	localBinDir := filepath.Join(home, ".local", "bin")
	os.MkdirAll(localBinDir, 0755)

	userHomeDirFn = func() (string, error) { return home, nil }
	statFn = os.Stat

	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", home)

	env := Capture()
	if !strings.Contains(env["PATH"], localBinDir) {
		t.Errorf("PATH should contain local bin dir: %s", env["PATH"])
	}
}

func TestCaptureHomebrewInPath(t *testing.T) {
	origHome := userHomeDirFn
	origStat := statFn
	defer func() {
		userHomeDirFn = origHome
		statFn = origStat
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Mock statFn so /opt/homebrew/bin "exists"
	statFn = func(name string) (os.FileInfo, error) {
		if name == "/opt/homebrew/bin" {
			// Return info for any existing dir
			return os.Stat(home)
		}
		return os.Stat(name)
	}

	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", home)

	env := Capture()
	if !strings.Contains(env["PATH"], "/opt/homebrew/bin") {
		t.Errorf("PATH should contain /opt/homebrew/bin: %s", env["PATH"])
	}
}

func TestCaptureNoDuplicatePaths(t *testing.T) {
	origHome := userHomeDirFn
	origStat := statFn
	defer func() {
		userHomeDirFn = origHome
		statFn = origStat
	}()

	home := t.TempDir()
	localBinDir := filepath.Join(home, ".local", "bin")
	os.MkdirAll(localBinDir, 0755)

	userHomeDirFn = func() (string, error) { return home, nil }
	statFn = os.Stat

	// PATH already contains the local bin dir
	t.Setenv("PATH", localBinDir+":/usr/bin")
	t.Setenv("HOME", home)

	env := Capture()
	count := strings.Count(env["PATH"], localBinDir)
	if count != 1 {
		t.Errorf("localBinDir should appear once in PATH, appeared %d times: %s", count, env["PATH"])
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
	}

	for key := range result {
		if !knownKeys[key] {
			t.Errorf("unexpected key %q in result", key)
		}
	}
}
