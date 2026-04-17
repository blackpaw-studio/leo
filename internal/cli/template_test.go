package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
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

// withTemplateTTY stubs the TTY check. fn returns whatever the test needs.
func withTemplateTTY(t *testing.T, isTTY bool) {
	t.Helper()
	old := templateIsTTY
	templateIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { templateIsTTY = old })
}

// withTemplateInput stubs the interactive reader for prompt-based tests.
func withTemplateInput(t *testing.T, input string) {
	t.Helper()
	old := templateStdin
	templateStdin = bufio.NewReader(strings.NewReader(input))
	t.Cleanup(func() { templateStdin = old })
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

// runListJSON runs `template list --json` and returns the decoded payload.
func runListJSON(t *testing.T) []templateListEntry {
	t.Helper()
	oldStdout := templateStdout
	var buf bytes.Buffer
	// Reuse a pipe so encoding/json's io.Writer contract is satisfied even
	// though we actually want a buffer. Direct swap works because templateStdout
	// is used as io.Writer inside the RunE.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	templateStdout = w

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	cmd := newTemplateListCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	_ = w.Close()
	<-done
	templateStdout = oldStdout

	var entries []templateListEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("decode json: %v; raw: %s", err, buf.String())
	}
	return entries
}

func TestTemplateList_JSONIncludesColumns(t *testing.T) {
	newTestConfigWithTemplates(t)
	entries := runListJSON(t)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	// Sorted by name → coding, research.
	if entries[0].Name != "coding" || entries[0].Model != "opus" || entries[0].Agent != "dev" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Name != "research" || entries[1].Model != "sonnet" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestTemplateList_JSONEmpty(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	entries := runListJSON(t)
	if len(entries) != 0 {
		t.Errorf("want 0 entries for empty config, got %d", len(entries))
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

func TestTemplateShow_ResolvedAppliesDefaults(t *testing.T) {
	// Template 'research' only sets Model=sonnet. Resolved output should
	// inherit MaxTurns from defaults.
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	trueVal := true
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{
			Model:              "sonnet",
			MaxTurns:           25,
			BypassPermissions:  trueVal,
			PermissionMode:     "acceptEdits",
			AllowedTools:       []string{"Read"},
			AppendSystemPrompt: "be concise",
		},
		Templates: map[string]config.TemplateConfig{
			"research": {}, // all unset — should inherit
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	out := captureStdout(t, func() {
		cmd := newTemplateShowCmd()
		if err := cmd.Flags().Set("resolved", "true"); err != nil {
			t.Fatalf("set resolved: %v", err)
		}
		if err := cmd.RunE(cmd, []string{"research"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	for _, want := range []string{
		"(resolved)",
		"sonnet",              // model inherited
		"25",                  // max_turns inherited
		"acceptEdits",         // permission_mode inherited
		"Read",                // allowed_tools inherited
		"be concise",          // append_system_prompt inherited
		"Bypass permissions:", // shown when resolved
		"true",                // bypass_permissions cascaded
	} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved output missing %q; got:\n%s", want, out)
		}
	}
}

func TestTemplateShow_ResolvedRemoteControlMatchesSpawner(t *testing.T) {
	// Regression test: resolveTemplate must match internal/agent/args.go,
	// which defaults remote_control to true for templates when unset —
	// independent of cfg.Defaults.RemoteControl. Previously the resolver
	// fell back to cfg.Defaults.RemoteControl and reported `false` while
	// the actual spawn sent `--remote-control`.
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	falseVal := false
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{
			Model:          "sonnet",
			MaxTurns:       25,
			RemoteControl:  false, // irrelevant for templates
			PermissionMode: "acceptEdits",
		},
		Templates: map[string]config.TemplateConfig{
			"unset":    {}, // tmpl.RemoteControl == nil → should resolve true
			"optedout": {RemoteControl: &falseVal},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	unset := captureStdout(t, func() {
		cmd := newTemplateShowCmd()
		if err := cmd.Flags().Set("resolved", "true"); err != nil {
			t.Fatalf("set resolved: %v", err)
		}
		if err := cmd.RunE(cmd, []string{"unset"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(unset, "Remote control:        true") {
		t.Errorf("unset template should resolve remote_control=true; got:\n%s", unset)
	}

	optedout := captureStdout(t, func() {
		cmd := newTemplateShowCmd()
		if err := cmd.Flags().Set("resolved", "true"); err != nil {
			t.Fatalf("set resolved: %v", err)
		}
		if err := cmd.RunE(cmd, []string{"optedout"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(optedout, "Remote control:        false") {
		t.Errorf("opted-out template should resolve remote_control=false; got:\n%s", optedout)
	}
}

func TestTemplateShow_ResolvedTemplateOverridesDefaults(t *testing.T) {
	// Template explicitly sets Model=opus; resolved output should keep it.
	newTestConfigWithTemplates(t)
	out := captureStdout(t, func() {
		cmd := newTemplateShowCmd()
		if err := cmd.Flags().Set("resolved", "true"); err != nil {
			t.Fatalf("set resolved: %v", err)
		}
		if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Model:                 opus") {
		t.Errorf("resolved should show template-overridden model; got:\n%s", out)
	}
}

// runShowJSON runs `template show --json` for name and returns decoded payload.
func runShowJSON(t *testing.T, name string, resolved bool) map[string]any {
	t.Helper()
	oldStdout := templateStdout
	var buf bytes.Buffer
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	templateStdout = w

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	cmd := newTemplateShowCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json: %v", err)
	}
	if resolved {
		if err := cmd.Flags().Set("resolved", "true"); err != nil {
			t.Fatalf("set resolved: %v", err)
		}
	}
	if err := cmd.RunE(cmd, []string{name}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	_ = w.Close()
	<-done
	templateStdout = oldStdout

	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v; raw: %s", err, buf.String())
	}
	return payload
}

func TestTemplateShow_JSONLiteral(t *testing.T) {
	newTestConfigWithTemplates(t)
	p := runShowJSON(t, "coding", false)
	if p["name"] != "coding" {
		t.Errorf("want name=coding, got %v", p["name"])
	}
	if p["model"] != "opus" {
		t.Errorf("want model=opus, got %v", p["model"])
	}
	// max_turns unset on template → omitempty should drop it.
	if _, ok := p["max_turns"]; ok {
		t.Errorf("literal JSON should omit max_turns when unset; got %v", p["max_turns"])
	}
}

func TestTemplateShow_JSONResolvedIncludesDefaults(t *testing.T) {
	newTestConfigWithTemplates(t)
	p := runShowJSON(t, "research", true)
	if p["name"] != "research" {
		t.Errorf("want name=research, got %v", p["name"])
	}
	// Resolved should have MaxTurns cascaded from defaults.
	if v, ok := p["max_turns"].(float64); !ok || int(v) != 10 {
		t.Errorf("resolved JSON should carry max_turns=10 from defaults; got %v", p["max_turns"])
	}
}

func TestTemplateRemove_DeletesAndPersistsWithYes(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)

	cmd := newTemplateRemoveCmd()
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
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

func TestTemplateRemove_ConfirmYes(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)
	withTemplateTTY(t, true)
	withTemplateInput(t, "y\n")

	cmd := newTemplateRemoveCmd()
	if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.Templates["coding"]; ok {
		t.Error("template 'coding' should have been removed after 'y'")
	}
}

func TestTemplateRemove_ConfirmNo(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)
	withTemplateTTY(t, true)
	withTemplateInput(t, "n\n")

	cmd := newTemplateRemoveCmd()
	if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.Templates["coding"]; !ok {
		t.Error("template 'coding' should still exist after 'n'")
	}
}

func TestTemplateRemove_NonTTYRequiresYes(t *testing.T) {
	newTestConfigWithTemplates(t)
	withTemplateTTY(t, false)

	cmd := newTemplateRemoveCmd()
	err := cmd.RunE(cmd, []string{"coding"})
	if err == nil {
		t.Fatal("expected error when non-TTY and --yes not set")
	}
	if !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("error should mention non-interactive; got %v", err)
	}
}

func TestTemplateRemove_NotFoundErrors(t *testing.T) {
	newTestConfigWithTemplates(t)
	cmd := newTemplateRemoveCmd()
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
	err := cmd.RunE(cmd, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error; got %v", err)
	}
}

func TestTemplateAdd_FlagDriven(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)
	withTemplateTTY(t, false) // non-interactive; flags only

	cmd := newTemplateAddCmd()
	for k, v := range map[string]string{
		"name":      "ops",
		"model":     "haiku",
		"agent":     "ops-agent",
		"workspace": "/tmp/ops",
	} {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := cfg.Templates["ops"]
	if !ok {
		t.Fatal("template 'ops' not created")
	}
	if got.Model != "haiku" || got.Agent != "ops-agent" || got.Workspace != "/tmp/ops" {
		t.Errorf("unexpected template config: %+v", got)
	}
}

func TestTemplateAdd_RequiresName(t *testing.T) {
	newTestConfigWithTemplates(t)
	withTemplateTTY(t, false)

	cmd := newTemplateAddCmd()
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error when --name missing")
	}
	if !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("error should mention --name; got %v", err)
	}
}

func TestTemplateAdd_RejectsDuplicate(t *testing.T) {
	newTestConfigWithTemplates(t)
	withTemplateTTY(t, false)

	cmd := newTemplateAddCmd()
	if err := cmd.Flags().Set("name", "coding"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate error; got %v", err)
	}
}

func TestTemplateAdd_InteractivePromptsForMissing(t *testing.T) {
	cfgPath := newTestConfigWithTemplates(t)
	withTemplateTTY(t, true)
	// Six prompts in order: name, workspace, channels, model, agent, permission-mode.
	withTemplateInput(t, "ops\n\n\nhaiku\nops-agent\n\n")

	cmd := newTemplateAddCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := cfg.Templates["ops"]
	if !ok {
		t.Fatal("template 'ops' not created")
	}
	if got.Model != "haiku" || got.Agent != "ops-agent" {
		t.Errorf("unexpected template config: %+v", got)
	}
}

func TestTemplateAdd_RejectsInvalidModel(t *testing.T) {
	newTestConfigWithTemplates(t)
	withTemplateTTY(t, false)

	cmd := newTemplateAddCmd()
	_ = cmd.Flags().Set("name", "bad")
	_ = cmd.Flags().Set("model", "gpt-4") // not in validModels
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected validation error for unknown model")
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
