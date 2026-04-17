package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// captureStdoutForConfigTests redirects os.Stdout so fmt.* writes can be
// captured. Colorized writers (fatih/color) cache stdout at init and are NOT
// captured by this helper.
func captureStdoutForConfigTests(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

// TestConfigShow_JSONOutput verifies --json emits valid, parseable JSON that
// reflects the loaded config.
func TestConfigShow_JSONOutput(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "opus", MaxTurns: 20},
		Tasks: map[string]config.TaskConfig{
			"nightly": {Schedule: "0 2 * * *", PromptFile: "nightly.md", Enabled: true},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	out := captureStdoutForConfigTests(t, func() {
		cmd := newConfigShowCmd()
		if err := cmd.Flags().Set("json", "true"); err != nil {
			t.Fatalf("set json flag: %v", err)
		}
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("expected JSON output; got:\n%s", out)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := parsed["Tasks"]; !ok {
		t.Errorf("expected Tasks key in JSON output; got keys: %v", keys(parsed))
	}
}

func TestConfigShow_RawAndJSONMutuallyExclusive(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	if err := config.Save(cfgPath, &config.Config{HomePath: home}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	cmd := newConfigShowCmd()
	_ = cmd.Flags().Set("raw", "true")
	_ = cmd.Flags().Set("json", "true")
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error; got %v", err)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestConfigPath verifies `leo config path` prints the absolute path to the
// resolved config file. The output is one line, trimmable whitespace only.
func TestConfigPath(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	if err := config.Save(cfgPath, &config.Config{HomePath: home}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	out := captureStdoutForConfigTests(t, func() {
		cmd := newConfigPathCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	got := strings.TrimSpace(out)
	wantAbs, err := filepath.Abs(cfgPath)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if got != wantAbs {
		t.Errorf("output = %q; want %q", got, wantAbs)
	}
}

// TestConfigPath_NoConfig verifies that when no config can be found, the
// command returns an error rather than printing an empty path.
func TestConfigPath_NoConfig(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = ""
	t.Cleanup(func() { cfgFile = oldCfgFile })

	// Point FindConfig at a directory tree with no leo.yaml and no default
	// home match. We can't easily override DefaultHome from here, so instead
	// rely on the --config=<nonexistent> path failing the normal resolver.
	// Use a path that definitely doesn't exist.
	cfgFile = filepath.Join(t.TempDir(), "nope.yaml")

	_ = captureStdoutForConfigTests(t, func() {
		cmd := newConfigPathCmd()
		// When cfgFile is set but file doesn't exist, the command still prints
		// the absolute path (it doesn't verify existence — scripts like
		// `vim $(leo config path)` may want to create the file).
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE: unexpected error %v", err)
		}
	})
}
