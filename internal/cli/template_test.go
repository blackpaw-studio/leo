package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// newTestConfigWithTemplates writes a minimal config containing templates and
// wires up cfgFile so loadConfig/saveConfig target it.
func newTestConfigWithTemplates(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Templates: map[string]config.TemplateConfig{
			"coding":   {Model: "opus", Agent: "dev", Workspace: "/tmp/coding"},
			"research": {Model: "sonnet"},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })
	return cfgPath
}

// captureStdout temporarily redirects os.Stdout and returns whatever is
// written via fmt.* writers. Note: colorized writers from fatih/color cache
// the original stdout at init, so info.Println output is NOT captured — tests
// that need to assert on colorized output should avoid this helper.
func captureStdout(t *testing.T, fn func()) string {
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

func TestTemplateList_ShowsAllTemplates(t *testing.T) {
	newTestConfigWithTemplates(t)

	out := captureStdout(t, func() {
		cmd := newTemplateListCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	for _, want := range []string{"NAME", "coding", "research", "opus", "sonnet"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q; got:\n%s", want, out)
		}
	}
}

func TestTemplateList_EmptyReturnsNoError(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	cmd := newTemplateListCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE should succeed on empty config; got %v", err)
	}
}

func TestTemplateShow_PrintsTemplateFields(t *testing.T) {
	newTestConfigWithTemplates(t)

	out := captureStdout(t, func() {
		cmd := newTemplateShowCmd()
		if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	for _, want := range []string{"Template: coding", "opus", "dev", "/tmp/coding"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q; got:\n%s", want, out)
		}
	}
}

func TestTemplateShow_NotFoundErrors(t *testing.T) {
	newTestConfigWithTemplates(t)
	cmd := newTemplateShowCmd()
	err := cmd.RunE(cmd, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error; got %v", err)
	}
}

func TestTemplateRemove_DeletesAndPersists(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)

	cmd := newTemplateRemoveCmd()
	if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Templates["coding"]; ok {
		t.Error("template 'coding' should have been removed")
	}
	if _, ok := cfg.Templates["research"]; !ok {
		t.Error("template 'research' should still be present")
	}
}

func TestTemplateRemove_NotFoundErrors(t *testing.T) {
	newTestConfigWithTemplates(t)
	cmd := newTemplateRemoveCmd()
	err := cmd.RunE(cmd, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error; got %v", err)
	}
}

func TestCompleteTemplateNames(t *testing.T) {
	newTestConfigWithTemplates(t)
	names, _ := completeTemplateNames(nil, nil, "co")
	found := false
	for _, n := range names {
		if n == "coding" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'coding' in completions; got %v", names)
	}
}
