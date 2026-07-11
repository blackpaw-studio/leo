//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/session"
)

// readStoredTaskSessionID returns the persisted harness session/thread ID
// for the given task name, or "" if not found.
func readStoredTaskSessionID(t *testing.T, ws, taskName string) string {
	t.Helper()
	store := session.NewStore(ws)
	id, _, err := store.Get("task:" + taskName)
	if err != nil {
		t.Fatalf("reading session store: %v", err)
	}
	return id
}

const codexTaskConfig = `tasks:
  cx:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/CODEX.md
    enabled: true
    harness: codex
    model: gpt-5.3-codex
    harness_options:
      sandbox: workspace-write
`

func TestCodexTaskRun(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	ws := setupWorkspace(t, codexTaskConfig, map[string]string{
		"prompts/CODEX.md": "Reply with exactly: pong\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, stderr, code := runLeo(t, ws, []string{
		"FAKECODEX_SCENARIO=success",
		"FAKECODEX_ARGLOG=" + argLog,
	}, "run", "cx", "-c", filepath.Join(ws, "leo.yaml"))
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr)
	}

	args := readArgLog(t, argLog)
	want := []string{
		"exec", "--json", "--skip-git-repo-check",
		"--model", "gpt-5.3-codex",
		"--sandbox", "workspace-write",
		"Reply with exactly: pong\n",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", args, want)
	}

	sid := readStoredTaskSessionID(t, ws, "cx")
	if sid != "thread_fake_1" {
		t.Errorf("stored session id = %q, want %q", sid, "thread_fake_1")
	}

	// Second run should resume the persisted thread.
	argLog2 := filepath.Join(t.TempDir(), "args2.json")
	_, stderr2, code2 := runLeo(t, ws, []string{
		"FAKECODEX_SCENARIO=success",
		"FAKECODEX_ARGLOG=" + argLog2,
	}, "run", "cx", "-c", filepath.Join(ws, "leo.yaml"))
	if code2 != 0 {
		t.Fatalf("second run: expected exit 0, got %d: %s", code2, stderr2)
	}

	args2 := readArgLog(t, argLog2)
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, "resume thread_fake_1") {
		t.Errorf("second run args = %v, want to contain %q", args2, "resume thread_fake_1")
	}
}

func TestCodexTaskErrorRecorded(t *testing.T) {
	ws := setupWorkspace(t, codexTaskConfig, map[string]string{
		"prompts/CODEX.md": "Reply with exactly: pong\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECODEX_SCENARIO=error",
	}, "run", "cx", "-c", filepath.Join(ws, "leo.yaml"))
	if code == 0 {
		t.Fatal("expected non-zero exit code when codex reports a fatal error")
	}

	hist := history.NewStore(ws)
	entry := hist.Get("cx")
	if entry == nil {
		t.Fatal("expected a history entry for task cx")
	}
	if entry.Reason != history.ReasonFailure {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonFailure)
	}
}

const opencodeTaskConfig = `tasks:
  oc:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/OPENCODE.md
    enabled: true
    harness: opencode
    model: anthropic/claude-sonnet-4-5
    harness_options:
      permission:
        bash: allow
`

func TestOpencodeTaskRun(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")
	envLog := filepath.Join(t.TempDir(), "env.json")

	ws := setupWorkspace(t, opencodeTaskConfig, map[string]string{
		"prompts/OPENCODE.md": "Reply with exactly: pong\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, stderr, code := runLeo(t, ws, []string{
		"FAKEOPENCODE_SCENARIO=success",
		"FAKEOPENCODE_ARGLOG=" + argLog,
		"FAKEOPENCODE_ENVLOG=" + envLog,
	}, "run", "oc", "-c", filepath.Join(ws, "leo.yaml"))
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr)
	}

	args := readArgLog(t, argLog)
	want := []string{
		"run", "--format", "json",
		"--model", "anthropic/claude-sonnet-4-5",
		"Reply with exactly: pong\n",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args = %#v, want %#v", args, want)
	}

	envMap := readEnvLog(t, envLog)
	content, ok := envMap["OPENCODE_CONFIG_CONTENT"]
	if !ok {
		t.Fatal("expected OPENCODE_CONFIG_CONTENT to be set")
	}
	if !strings.Contains(content, `"permission"`) || !strings.Contains(content, `"bash":"allow"`) {
		t.Errorf("OPENCODE_CONFIG_CONTENT = %q, want to contain the permission block", content)
	}

	sid := readStoredTaskSessionID(t, ws, "oc")
	if sid != "ses_fake000000000000000000001" {
		t.Errorf("stored session id = %q, want %q", sid, "ses_fake000000000000000000001")
	}

	// Second run should resume the persisted session via -s.
	argLog2 := filepath.Join(t.TempDir(), "args2.json")
	_, stderr2, code2 := runLeo(t, ws, []string{
		"FAKEOPENCODE_SCENARIO=success",
		"FAKEOPENCODE_ARGLOG=" + argLog2,
	}, "run", "oc", "-c", filepath.Join(ws, "leo.yaml"))
	if code2 != 0 {
		t.Fatalf("second run: expected exit 0, got %d: %s", code2, stderr2)
	}

	args2 := readArgLog(t, argLog2)
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, "-s ses_fake000000000000000000001") {
		t.Errorf("second run args = %v, want to contain %q", args2, "-s ses_fake000000000000000000001")
	}
}

func TestOpencodeTruncatedStreamStillSucceeds(t *testing.T) {
	ws := setupWorkspace(t, opencodeTaskConfig, map[string]string{
		"prompts/OPENCODE.md": "Reply with exactly: pong\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, stderr, code := runLeo(t, ws, []string{
		"FAKEOPENCODE_SCENARIO=truncated",
	}, "run", "oc", "-c", filepath.Join(ws, "leo.yaml"))
	if code != 0 {
		t.Fatalf("expected exit 0 despite truncated stream (EOF-as-turn-end), got %d: %s", code, stderr)
	}

	sid := readStoredTaskSessionID(t, ws, "oc")
	if sid != "ses_fake000000000000000000001" {
		t.Errorf("stored session id = %q, want %q", sid, "ses_fake000000000000000000001")
	}
}

// TestNonClaudeValidationErrors covers a harness/scope combination that
// still fails validation after Plan 4 Task 5 (codex TurnDriver): codex
// processes are now supported, but channel plugins never are for codex — it
// has no channel-plugin concept, only leo's MCP tools.
func TestNonClaudeValidationErrors(t *testing.T) {
	const cfg = `processes:
  worker:
    workspace: %s
    harness: codex
    channels:
      - plugin:telegram@claude-plugins-official
`
	ws := setupWorkspace(t, cfg, nil)
	fixWorkspaceInConfig(t, ws)

	stdout, stderr, code := runLeo(t, ws, nil, "validate", "-c", filepath.Join(ws, "leo.yaml"))
	if code == 0 {
		t.Fatal("expected non-zero exit for a config with an invalid harness/process combination")
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "does not support channel plugins") {
		t.Errorf("validate output = %q, want to mention that the codex harness does not support channel plugins", combined)
	}
}

// TestCodexProcessValidatesClean locks in that a codex process/template/
// session with no channels now passes validation cleanly (Plan 4 Task 5
// TurnDriver + Plan 4 Task 7 session drivers).
func TestCodexProcessValidatesClean(t *testing.T) {
	const cfg = `processes:
  worker:
    workspace: %s
    harness: codex
templates:
  helper:
    harness: codex
sessions:
  chat:
    workspace: %s
    harness: codex
`
	ws := setupWorkspace(t, cfg, nil)
	fixWorkspaceInConfig(t, ws)

	stdout, stderr, code := runLeo(t, ws, nil, "validate", "-c", filepath.Join(ws, "leo.yaml"))
	combined := stdout + stderr
	if code != 0 {
		t.Fatalf("expected clean validation, got exit %d: %s", code, combined)
	}
	if strings.Contains(combined, "cannot run persistent sessions yet") {
		t.Errorf("validate output = %q, must not reject the codex session anymore", combined)
	}
	if strings.Contains(combined, "cannot run supervised processes yet") {
		t.Errorf("validate output = %q, must not reject the codex process anymore", combined)
	}
	if strings.Contains(combined, "cannot run ephemeral agents yet") {
		t.Errorf("validate output = %q, must not reject the codex template anymore", combined)
	}
}

// TestRealHarnessSmoke* run the real codex/opencode CLIs end-to-end. They
// never run in CI: skipped unless the real binary is on PATH (constructed
// WITHOUT the fake-binary dir, so the fakes don't shadow the real thing)
// AND LEO_E2E_REAL_HARNESSES=1 is set. Real runs cost API money.

// No model is pinned in either smoke config: model names are account- and
// install-specific (a hardcoded opencode model hit ProviderModelNotFoundError
// on a machine whose registry lacked it), and the smoke's job is to verify
// the leo↔harness integration, not model selection. The cross-harness model
// cascade resolves to "" here, so each CLI uses its own default model.
func TestRealHarnessSmokeCodex(t *testing.T) {
	realSmokeTest(t, "codex", "cx", "harness: codex\n")
}

func TestRealHarnessSmokeOpencode(t *testing.T) {
	realSmokeTest(t, "opencode", "oc", "harness: opencode\n")
}

// realSmokeTest is the shared body for the gated real-binary smoke tests.
func realSmokeTest(t *testing.T, binary, taskName, harnessYAML string) {
	t.Helper()

	if os.Getenv("LEO_E2E_REAL_HARNESSES") != "1" {
		t.Skip("set LEO_E2E_REAL_HARNESSES=1 to run real-binary smoke tests (costs API money; never runs in CI)")
	}

	// Look up the real binary on a PATH that excludes the fake-binary dir,
	// so a fake of the same name doesn't shadow it.
	realPath := strings.Join(filterOutDir(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)), filepath.Dir(fakeclaude)), string(os.PathListSeparator))
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", realPath)
	lp, err := exec.LookPath(binary)
	os.Setenv("PATH", origPath)
	if err != nil || lp == "" {
		t.Skipf("real %s binary not found on PATH (excluding fake dir): %v", binary, err)
	}

	cfg := "tasks:\n  " + taskName + ":\n    workspace: %s\n    schedule: \"0 9 * * *\"\n    prompt_file: prompts/SMOKE.md\n    enabled: true\n    " + harnessYAML

	ws := setupWorkspace(t, cfg, map[string]string{
		"prompts/SMOKE.md": "Reply with exactly: pong\n",
	})
	fixWorkspaceInConfig(t, ws)

	cmd := exec.Command(leoBin, "run", taskName, "-c", filepath.Join(ws, "leo.yaml"))
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "PATH="+realPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("failed to run leo: %v", err)
		}
	}
	code := cmd.ProcessState.ExitCode()
	if code != 0 {
		t.Fatalf("expected exit 0 from real %s smoke run, got %d: %s", binary, code, out)
	}

	sid := readStoredTaskSessionID(t, ws, taskName)
	if sid == "" {
		t.Errorf("expected a non-empty stored session id after the real %s smoke run", binary)
	}
}

// filterOutDir returns dirs with any entry equal to exclude removed.
func filterOutDir(dirs []string, exclude string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == exclude {
			continue
		}
		out = append(out, d)
	}
	return out
}
