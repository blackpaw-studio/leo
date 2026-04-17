package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, c ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := splitAndTrim(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitAndTrim(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// newTestConfig writes a minimal config to a tmp home and sets cfgFile so
// loadConfig/saveConfig target it.
func newTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Processes: map[string]config.ProcessConfig{
			"existing": {Enabled: true, Model: "sonnet"},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// loadConfig reads via FindConfig — use explicit cfgFile.
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })
	return cfg, cfgPath
}

func TestSetProcessEnabled_TogglesAndPersists(t *testing.T) {
	_, cfgPath := newTestConfig(t)

	if err := setProcessEnabled("existing", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Processes["existing"].Enabled {
		t.Errorf("process should be disabled after setProcessEnabled(false)")
	}

	if err := setProcessEnabled("existing", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, _ = config.Load(cfgPath)
	if !got.Processes["existing"].Enabled {
		t.Errorf("process should be enabled after setProcessEnabled(true)")
	}
}

func TestSetProcessEnabled_MissingProcess(t *testing.T) {
	newTestConfig(t)
	err := setProcessEnabled("does-not-exist", true)
	if err == nil {
		t.Fatal("expected error for missing process")
	}
}

// Verify saveConfig writes to the resolved configPath.
func TestSaveConfig_WritesToResolvedPath(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Processes["new"] = config.ProcessConfig{Enabled: true}

	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("config file is empty after save")
	}
}

// withStubProcessStdio swaps processStdin/processStdout for the duration of
// a test so interactive prompts and status messages can be captured without
// touching the real terminal.
func withStubProcessStdio(t *testing.T, stdin io.Reader) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	oldIn, oldOut := processStdin, processStdout
	processStdin = stdin
	processStdout = &out
	t.Cleanup(func() {
		processStdin = oldIn
		processStdout = oldOut
	})
	return &out
}

// withProcessTTY overrides the TTY probe so tests can pick interactive vs
// non-interactive modes deterministically.
func withProcessTTY(t *testing.T, isTTY bool) {
	t.Helper()
	old := processIsTTY
	processIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { processIsTTY = old })
}

func TestProcessList_JSON_EmptyProcesses(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	// Wipe processes so we assert the empty-case JSON output.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Processes = nil
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out := withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var entries []processListEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v — raw: %s", err, out.String())
	}
	if len(entries) != 0 {
		t.Errorf("expected empty list, got %v", entries)
	}
}

func TestProcessList_JSON_ReflectsConfig(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Processes["bot-a"] = config.ProcessConfig{
		Enabled:   true,
		Model:     "opus",
		Workspace: "/tmp/bot-a",
		Channels:  []string{"plugin:telegram@claude-plugins-official"},
	}
	cfg.Processes["bot-b"] = config.ProcessConfig{
		Enabled: false,
		Model:   "sonnet",
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out := withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var entries []processListEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v — raw: %s", err, out.String())
	}

	// Deterministic order — alphabetical.
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name }) {
		t.Errorf("entries not sorted by name: %v", entries)
	}

	byName := map[string]processListEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	a, ok := byName["bot-a"]
	if !ok {
		t.Fatalf("missing bot-a in %v", entries)
	}
	if a.Model != "opus" || !a.Enabled || a.Status != "enabled" {
		t.Errorf("bot-a unexpected: %+v", a)
	}
	if len(a.Channels) != 1 || a.Channels[0] != "plugin:telegram@claude-plugins-official" {
		t.Errorf("bot-a channels unexpected: %v", a.Channels)
	}

	b, ok := byName["bot-b"]
	if !ok {
		t.Fatalf("missing bot-b in %v", entries)
	}
	if b.Enabled || b.Status != "disabled" {
		t.Errorf("bot-b should be disabled: %+v", b)
	}
	if b.Runtime != nil {
		t.Errorf("no runtime state expected without daemon: %+v", b.Runtime)
	}
}

func TestProcessRemove_NonTTYWithoutYes_Errors(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, false)
	withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "remove", "existing"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when non-TTY without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should mention --yes, got %v", err)
	}

	// Config must be untouched.
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Processes["existing"]; !ok {
		t.Errorf("process should still exist after aborted remove")
	}
}

func TestProcessRemove_YesFlag_SkipsPrompt(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, false)
	withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "remove", "existing", "--yes"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Processes["existing"]; ok {
		t.Errorf("process should be removed after --yes")
	}
}

func TestProcessRemove_TTYPromptYes_Removes(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, true)
	// prompt.YesNo default=false for remove, so "y\n" is required to confirm.
	withStubProcessStdio(t, strings.NewReader("y\n"))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "remove", "existing"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Processes["existing"]; ok {
		t.Errorf("process should be removed after 'y' confirmation")
	}
}

func TestProcessRemove_TTYPromptNo_KeepsProcess(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, true)
	// Default is "no", so hitting Enter without input must cancel.
	withStubProcessStdio(t, strings.NewReader("\n"))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "remove", "existing"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Processes["existing"]; !ok {
		t.Errorf("process should still exist after declining prompt")
	}
}

func TestProcessRemove_MissingProcess(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, true)
	withStubProcessStdio(t, strings.NewReader("y\n"))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "remove", "ghost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for missing process")
	}
}

func TestProcessAdd_NonTTY_UsesFlagsOnly(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, false)
	withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{
		"--config", cfgPath,
		"process", "add", "scripted",
		"--model", "haiku",
		"--channels", "plugin:foo@bar",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	proc, ok := got.Processes["scripted"]
	if !ok {
		t.Fatalf("process 'scripted' not added")
	}
	if proc.Model != "haiku" {
		t.Errorf("model = %q, want haiku", proc.Model)
	}
	if len(proc.Channels) != 1 || proc.Channels[0] != "plugin:foo@bar" {
		t.Errorf("channels = %v, want [plugin:foo@bar]", proc.Channels)
	}
	if proc.Workspace != "" {
		t.Errorf("workspace = %q, want empty (non-TTY, no flag)", proc.Workspace)
	}
	if proc.Agent != "" {
		t.Errorf("agent = %q, want empty (non-TTY, no flag)", proc.Agent)
	}
	if !proc.Enabled {
		t.Errorf("process should be enabled by default")
	}
}

func TestProcessAdd_TTY_PromptsOnlyForMissingFlags(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, true)
	// Model is supplied via --model, so the prompts are (in order):
	//   workspace, channels, agent — three prompts.
	// "\n" for each accepts the default/empty value.
	withStubProcessStdio(t, strings.NewReader("/tmp/mixed\n\npicky-agent\n"))

	root := newRootCmd()
	root.SetArgs([]string{
		"--config", cfgPath,
		"process", "add", "mixed",
		"--model", "opus",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	proc, ok := got.Processes["mixed"]
	if !ok {
		t.Fatalf("process 'mixed' not added")
	}
	if proc.Model != "opus" {
		t.Errorf("model = %q, want opus (flag wins)", proc.Model)
	}
	if proc.Workspace != "/tmp/mixed" {
		t.Errorf("workspace = %q, want '/tmp/mixed' (from prompt)", proc.Workspace)
	}
	if len(proc.Channels) != 0 {
		t.Errorf("channels = %v, want empty (blank prompt)", proc.Channels)
	}
	if proc.Agent != "picky-agent" {
		t.Errorf("agent = %q, want 'picky-agent' (from prompt)", proc.Agent)
	}
}

func TestProcessAdd_Exists_Errors(t *testing.T) {
	_, cfgPath := newTestConfig(t)
	withProcessTTY(t, false)
	withStubProcessStdio(t, strings.NewReader(""))

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "process", "add", "existing", "--model", "haiku"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error adding duplicate process")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention duplicate, got %v", err)
	}
}
