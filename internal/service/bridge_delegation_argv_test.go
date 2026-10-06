package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// delegationSpec is an agent whose stored args carry the nudge with its
// delegation block, then a user prompt, on --append-system-prompt.
func delegationSpec(t *testing.T) ProcessSpec {
	t.Helper()
	cfg := &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles:         map[string]config.RoleSpec{"implement": {UseFor: "write code"}},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "t"}}}},
	}}
	spec := claudeSpec(t, "alpha")
	spec.ClaudeArgs = append(spec.ClaudeArgs, "--append-system-prompt", leomcp.LeoNudge(cfg)+"\n\nuser instruction")
	return spec
}

// firstLaunchText is the first new-session call in the tmux log, every
// line of it: a system prompt on argv spans lines.
func firstLaunchText(t *testing.T, logPath string) string {
	t.Helper()
	waitForNewSessions(t, logPath, 1)
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	i := strings.Index(log, "new-session")
	if i < 0 {
		t.Fatalf("no new-session in tmux log:\n%s", log)
	}
	return log[i:]
}

// A bridged claude gets delegation live from the mod, so its launch argv
// drops the delegation block and keeps the rest of the system prompt; the
// stored args keep it for a legacy relaunch.
func TestBridgedLaunchDropsTheDelegationBlockFromArgv(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, delegationSpec(t))

	line := firstLaunchText(t, logPath)
	if !strings.Contains(line, "--plugin-dir") {
		t.Fatalf("expected a bridged launch:\n%s", line)
	}
	if strings.Contains(line, "Delegation roles:") || strings.Contains(line, "leo-delegation") {
		t.Fatalf("bridged argv carries the delegation block:\n%s", line)
	}
	for _, want := range []string{"'--append-system-prompt'", "leo_skill", "user instruction"} {
		if !strings.Contains(line, want) {
			t.Errorf("bridged argv lost %q:\n%s", want, line)
		}
	}
	if args := strings.Join(f.sv.identities["alpha"].Args(), " "); !strings.Contains(args, "Delegation roles:") {
		t.Fatalf("stored args lost the delegation block: %s", args)
	}
}

func TestLegacyLaunchKeepsTheDelegationBlockOnArgv(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	startBridged(t, tmuxPath, "2.1.286", time.Minute, delegationSpec(t))

	line := firstLaunchText(t, logPath)
	if strings.Contains(line, "--plugin-dir") {
		t.Fatalf("expected a legacy launch:\n%s", line)
	}
	if !strings.Contains(line, "Delegation roles:") || !strings.Contains(line, "user instruction") {
		t.Fatalf("legacy argv lost the delegation block:\n%s", line)
	}
}

// An agent record persisted before the delimiters carries the block bare;
// restored onto a bridged launch it loses it when the current config
// renders it exactly, or the frozen block outlives delegation being turned
// off.
func TestBridgedLaunchDropsALegacyRecordsUndelimitedBlock(t *testing.T) {
	cfg := &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles:         map[string]config.RoleSpec{"implement": {UseFor: "write code"}},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {}},
	}}
	cfg.Delegation.SetEnabled(false)
	configPath := filepath.Join(t.TempDir(), "leo.yaml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	legacy := "Load leo_skill for leo operations.\n\nDelegation roles:\n- implement: write code\n" +
		"Dispatch with `leo_dispatch(role: …)`; do not pick templates or models yourself.\n" +
		"Call `leo_delegation` if a role you expect is missing.\n\n\nuser instruction"
	spec.ClaudeArgs = append(spec.ClaudeArgs, "--append-system-prompt", legacy)
	startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, func(o *bridgeTestOpts) { o.configPath = configPath })

	line := firstLaunchText(t, logPath)
	if !strings.Contains(line, "--plugin-dir") {
		t.Fatalf("expected a bridged launch:\n%s", line)
	}
	if strings.Contains(line, "Delegation roles:") {
		t.Fatalf("bridged argv kept the legacy block:\n%s", line)
	}
	for _, want := range []string{"leo_skill", "user instruction"} {
		if !strings.Contains(line, want) {
			t.Errorf("bridged argv lost %q:\n%s", want, line)
		}
	}
}
