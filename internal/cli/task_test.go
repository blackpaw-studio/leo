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

// newTestTaskConfig writes a minimal config with a Tasks map to a tmp home
// and sets cfgFile so loadConfig/saveConfig target it. Returns the config
// path so tests can re-Load after mutations.
func newTestTaskConfig(t *testing.T, existing map[string]config.TaskConfig) string {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Tasks:    existing,
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })
	return cfgPath
}

// withStubIsTTY swaps taskIsTTY for the duration of the test.
func withStubIsTTY(t *testing.T, isTTY bool) {
	t.Helper()
	old := taskIsTTY
	taskIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { taskIsTTY = old })
}

// withStubYesNo swaps taskYesNo with a fixed answer.
func withStubYesNo(t *testing.T, answer bool) *int {
	t.Helper()
	calls := 0
	old := taskYesNo
	taskYesNo = func(_ *bufio.Reader, _ string, _ bool) bool {
		calls++
		return answer
	}
	t.Cleanup(func() { taskYesNo = old })
	return &calls
}

// withCapturedStdout pipes os.Stdout through a buffer for the duration of fn.
// Colorized writers (fatih/color) cache stdout at init and are NOT captured.
func withCapturedStdout(t *testing.T, fn func()) string {
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

// TestTaskAdd_FlagFirstNonInteractive covers the full-flag fast path: every
// required and optional field is supplied so no prompt should ever be read.
func TestTaskAdd_FlagFirstNonInteractive(t *testing.T) {
	cases := []struct {
		name       string
		flags      map[string]string
		wantTask   config.TaskConfig
		wantErrSub string
	}{
		{
			name: "required only",
			flags: map[string]string{
				"name":        "nightly",
				"schedule":    "0 7 * * *",
				"prompt-file": "prompts/nightly.md",
			},
			wantTask: config.TaskConfig{
				Schedule:   "0 7 * * *",
				PromptFile: "prompts/nightly.md",
				Enabled:    true,
			},
		},
		{
			name: "full flag set",
			flags: map[string]string{
				"name":           "digest",
				"schedule":       "0 8 * * 1-5",
				"prompt-file":    "prompts/digest.md",
				"model":          "opus",
				"channels":       "plugin:telegram@official, plugin:slack@official",
				"notify-on-fail": "true",
				"silent":         "true",
				"disabled":       "true",
			},
			wantTask: config.TaskConfig{
				Schedule:     "0 8 * * 1-5",
				PromptFile:   "prompts/digest.md",
				Model:        "opus",
				Channels:     []string{"plugin:telegram@official", "plugin:slack@official"},
				NotifyOnFail: true,
				Silent:       true,
				Enabled:      false,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := newTestTaskConfig(t, nil)
			// stdin must look like a non-TTY to force flag-first behavior for
			// any missing field — even though we supply all required flags.
			withStubIsTTY(t, false)

			cmd := newTaskAddCmd()
			for k, v := range tc.flags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatalf("flag %s=%s: %v", k, v, err)
				}
			}
			_ = withCapturedStdout(t, func() {
				if err := cmd.RunE(cmd, nil); err != nil {
					t.Fatalf("RunE: %v", err)
				}
			})

			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			got, ok := cfg.Tasks[tc.flags["name"]]
			if !ok {
				t.Fatalf("task %q not persisted; tasks=%v", tc.flags["name"], cfg.Tasks)
			}
			if got.Schedule != tc.wantTask.Schedule {
				t.Errorf("Schedule = %q, want %q", got.Schedule, tc.wantTask.Schedule)
			}
			if got.PromptFile != tc.wantTask.PromptFile {
				t.Errorf("PromptFile = %q, want %q", got.PromptFile, tc.wantTask.PromptFile)
			}
			if got.Model != tc.wantTask.Model {
				t.Errorf("Model = %q, want %q", got.Model, tc.wantTask.Model)
			}
			if got.NotifyOnFail != tc.wantTask.NotifyOnFail {
				t.Errorf("NotifyOnFail = %v, want %v", got.NotifyOnFail, tc.wantTask.NotifyOnFail)
			}
			if got.Silent != tc.wantTask.Silent {
				t.Errorf("Silent = %v, want %v", got.Silent, tc.wantTask.Silent)
			}
			if got.Enabled != tc.wantTask.Enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tc.wantTask.Enabled)
			}
			if len(got.Channels) != len(tc.wantTask.Channels) {
				t.Fatalf("Channels = %v, want %v", got.Channels, tc.wantTask.Channels)
			}
			for i := range got.Channels {
				if got.Channels[i] != tc.wantTask.Channels[i] {
					t.Errorf("Channels[%d] = %q, want %q", i, got.Channels[i], tc.wantTask.Channels[i])
				}
			}
		})
	}
}

// TestTaskAdd_NonTTY_MissingFlags_Errors covers the no-TTY guard: if required
// fields are missing we must error out with a clear message listing the
// missing flags, and must not mutate the config.
func TestTaskAdd_NonTTY_MissingFlags_Errors(t *testing.T) {
	cases := []struct {
		name        string
		flags       map[string]string
		wantMissing []string
	}{
		{
			name:        "no flags at all",
			flags:       map[string]string{},
			wantMissing: []string{"--name", "--schedule", "--prompt-file"},
		},
		{
			name:        "only name",
			flags:       map[string]string{"name": "foo"},
			wantMissing: []string{"--schedule", "--prompt-file"},
		},
		{
			name: "name + schedule",
			flags: map[string]string{
				"name":     "foo",
				"schedule": "@daily",
			},
			wantMissing: []string{"--prompt-file"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := newTestTaskConfig(t, nil)
			withStubIsTTY(t, false)

			cmd := newTaskAddCmd()
			for k, v := range tc.flags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatalf("flag %s=%s: %v", k, v, err)
				}
			}

			err := cmd.RunE(cmd, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, "missing required flag") {
				t.Errorf("expected 'missing required flag' in error; got: %s", msg)
			}
			for _, m := range tc.wantMissing {
				if !strings.Contains(msg, m) {
					t.Errorf("expected error to mention %q; got: %s", m, msg)
				}
			}

			// Config must be untouched.
			cfg, _ := config.Load(cfgPath)
			if len(cfg.Tasks) != 0 {
				t.Errorf("expected Tasks to stay empty on error; got %v", cfg.Tasks)
			}
		})
	}
}

// TestTaskAdd_DuplicateName errors instead of clobbering an existing task.
func TestTaskAdd_DuplicateName(t *testing.T) {
	cfgPath := newTestTaskConfig(t, map[string]config.TaskConfig{
		"nightly": {Schedule: "0 7 * * *", PromptFile: "p.md", Enabled: true},
	})
	withStubIsTTY(t, false)

	cmd := newTaskAddCmd()
	_ = cmd.Flags().Set("name", "nightly")
	_ = cmd.Flags().Set("schedule", "0 9 * * *")
	_ = cmd.Flags().Set("prompt-file", "other.md")

	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error; got %v", err)
	}

	// Existing task must be untouched.
	cfg, _ := config.Load(cfgPath)
	got := cfg.Tasks["nightly"]
	if got.Schedule != "0 7 * * *" || got.PromptFile != "p.md" {
		t.Errorf("existing task clobbered: %+v", got)
	}
}

// TestTaskList_JSON covers the --json code path: shape, fields, and empty.
func TestTaskList_JSON(t *testing.T) {
	newTestTaskConfig(t, map[string]config.TaskConfig{
		"nightly": {
			Schedule:   "0 7 * * *",
			PromptFile: "p.md",
			Model:      "opus",
			Enabled:    true,
		},
		"disabled-one": {
			Schedule:   "0 8 * * *",
			PromptFile: "d.md",
			Enabled:    false,
		},
	})

	cmd := newTaskListCmd()
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var entries []taskListEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}

	byName := map[string]taskListEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if got := byName["nightly"]; got.Schedule != "0 7 * * *" || got.Model != "opus" || !got.Enabled {
		t.Errorf("nightly = %+v", got)
	}
	if got := byName["disabled-one"]; got.Enabled {
		t.Errorf("disabled-one should be Enabled=false: %+v", got)
	}
	// Model falls back to defaults.model when the per-task model is empty.
	if got := byName["disabled-one"]; got.Model != "sonnet" {
		t.Errorf("disabled-one Model = %q, want fallback 'sonnet'", got.Model)
	}
}

// TestTaskList_JSON_Empty returns an empty array (not null), consistent with
// the `leo agent list --json` contract.
func TestTaskList_JSON_Empty(t *testing.T) {
	newTestTaskConfig(t, nil)

	cmd := newTaskListCmd()
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	trimmed := strings.TrimSpace(buf.String())
	if trimmed != "[]" {
		t.Errorf("want \"[]\"; got %q", trimmed)
	}
}

// TestTaskRemove_ConfirmFlow covers: yes, no, --yes, and non-TTY-without-yes.
func TestTaskRemove_ConfirmFlow(t *testing.T) {
	cases := []struct {
		name       string
		isTTY      bool
		answer     bool
		useYesFlag bool
		wantKept   bool
		wantErrSub string
	}{
		{
			name:     "tty yes confirms removal",
			isTTY:    true,
			answer:   true,
			wantKept: false,
		},
		{
			name:     "tty no aborts removal",
			isTTY:    true,
			answer:   false,
			wantKept: true,
		},
		{
			name:       "yes flag skips prompt",
			isTTY:      true,
			useYesFlag: true,
			wantKept:   false,
		},
		{
			name:       "non-tty without yes errors",
			isTTY:      false,
			wantKept:   true,
			wantErrSub: "without confirmation",
		},
		{
			name:       "non-tty with yes removes",
			isTTY:      false,
			useYesFlag: true,
			wantKept:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := newTestTaskConfig(t, map[string]config.TaskConfig{
				"doomed": {Schedule: "0 7 * * *", PromptFile: "p.md", Enabled: true},
			})
			withStubIsTTY(t, tc.isTTY)
			calls := withStubYesNo(t, tc.answer)

			cmd := newTaskRemoveCmd()
			if tc.useYesFlag {
				_ = cmd.Flags().Set("yes", "true")
			}

			err := withCapturedStdoutErr(t, func() error {
				return cmd.RunE(cmd, []string{"doomed"})
			})

			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("want error containing %q; got %v", tc.wantErrSub, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			cfg, _ := config.Load(cfgPath)
			_, stillThere := cfg.Tasks["doomed"]
			if stillThere != tc.wantKept {
				t.Errorf("task retention = %v, want %v (tasks=%v)", stillThere, tc.wantKept, cfg.Tasks)
			}

			// When --yes is used, the YesNo prompt must not be called.
			// When non-TTY path errors before prompting, it also must not be called.
			if tc.useYesFlag && *calls != 0 {
				t.Errorf("--yes path invoked YesNo %d time(s); want 0", *calls)
			}
			if !tc.isTTY && !tc.useYesFlag && *calls != 0 {
				t.Errorf("non-TTY path invoked YesNo %d time(s); want 0", *calls)
			}
		})
	}
}

// TestTaskRemove_NotFound surfaces a clean error without calling the prompt.
func TestTaskRemove_NotFound(t *testing.T) {
	newTestTaskConfig(t, map[string]config.TaskConfig{
		"other": {Schedule: "0 7 * * *", PromptFile: "p.md", Enabled: true},
	})
	withStubIsTTY(t, true)
	calls := withStubYesNo(t, true)

	cmd := newTaskRemoveCmd()
	err := cmd.RunE(cmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want 'not found' error; got %v", err)
	}
	if *calls != 0 {
		t.Errorf("YesNo should not be called for a missing task; got %d calls", *calls)
	}
}

// withCapturedStdoutErr runs fn while swallowing stdout (for commands that
// print success lines we don't want cluttering test output). Returns fn's
// error verbatim.
func withCapturedStdoutErr(t *testing.T, fn func() error) error {
	t.Helper()
	var out error
	_ = withCapturedStdout(t, func() {
		out = fn()
	})
	return out
}
