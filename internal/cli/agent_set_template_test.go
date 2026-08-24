package cli

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

func TestAgentSetTemplateRemote(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-template", "leo-coding-bar", "codex", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent set-template leo-coding-bar codex") {
		t.Errorf("unexpected call: %s", joined)
	}
	if !strings.Contains(joined, "--json") {
		t.Errorf("--json not forwarded to the remote: %s", joined)
	}
}

func TestAgentSetTemplateRequiresTwoArgs(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)

	for _, args := range [][]string{
		{"agent", "set-template"},
		{"agent", "set-template", "leo-coding-bar"},
		{"agent", "set-template", "leo-coding-bar", "codex", "extra"},
	} {
		root := newRootCmd()
		root.SetArgs(append([]string{"--config", path}, args...))
		if err := root.Execute(); err == nil {
			t.Errorf("expected an error for args %v", args)
		}
	}
}

// The target template is subject to the same can_spawn allowlist as
// `leo agent spawn`: a switch launches that template, so gating only the
// disruption permission would leave a way to reach a denied template.
func TestAgentSetTemplateHonorsSpawnAllowlist(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)
	t.Setenv("LEO_PERMISSIONS", `{"can_spawn":["review"]}`)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-template", "leo-coding-bar", "codex"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a permission error for a template outside can_spawn")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error should name the refused template: %v", err)
	}
}

func TestAgentSetTemplateHonorsStopDenial(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_stop_agent"]}`)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-template", "leo-coding-bar", "codex"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected a permission error when the template denies leo_stop_agent")
	}
}

func TestFormatSwitchResult(t *testing.T) {
	tests := []struct {
		name   string
		result agent.SwitchResult
		want   []string
	}{
		{
			name: "cross-harness fresh session",
			result: agent.SwitchResult{
				Name: "leo-x", FromTemplate: "coding", ToTemplate: "codex",
				FromHarness: "claude", ToHarness: "codex", Status: "running",
			},
			want: []string{"leo-x: coding → codex", "(claude → codex)", "respawned on a new session"},
		},
		{
			name: "same harness rejoined",
			result: agent.SwitchResult{
				Name: "leo-x", FromTemplate: "coding", ToTemplate: "review",
				FromHarness: "claude", ToHarness: "claude", Status: "running", Resumed: true,
			},
			want: []string{"leo-x: coding → review", "respawned, rejoined this template's previous session"},
		},
		{
			name: "stopped (dormant) agent",
			result: agent.SwitchResult{
				Name: "leo-x", FromTemplate: "coding", ToTemplate: "codex",
				FromHarness: "claude", ToHarness: "codex", Status: "stopped",
			},
			want: []string{"still stopped", "starts a new session on next start"},
		},
		{
			name: "no-op",
			result: agent.SwitchResult{
				Name: "leo-x", FromTemplate: "coding", ToTemplate: "coding",
				FromHarness: "claude", ToHarness: "claude", Status: "running", Unchanged: true,
			},
			want: []string{"already on template coding", "nothing changed"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSwitchResult(tc.result)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output %q missing %q", got, want)
				}
			}
		})
	}
}

// A same-harness switch should not print a harness clause — "(claude → claude)"
// is noise that makes the interesting case (a real harness change) harder to spot.
func TestFormatSwitchResultOmitsUnchangedHarness(t *testing.T) {
	got := formatSwitchResult(agent.SwitchResult{
		Name: "leo-x", FromTemplate: "coding", ToTemplate: "review",
		FromHarness: "claude", ToHarness: "claude", Status: "running",
	})
	if strings.Contains(got, "claude → claude") {
		t.Errorf("output should not restate an unchanged harness: %q", got)
	}
}

// Both doors onto a template switch — the verb and the attach picker's menu —
// go through gateTemplateSwitch, so the policy is asserted once, here.
func TestGateTemplateSwitch(t *testing.T) {
	tests := []struct {
		name        string
		permissions string
		template    string
		wantErr     string
	}{
		{name: "unrestricted", permissions: "", template: "codex"},
		{name: "allowed by can_spawn", permissions: `{"can_spawn":["codex","review"]}`, template: "codex"},
		{
			name:        "outside can_spawn",
			permissions: `{"can_spawn":["review"]}`,
			template:    "codex",
			wantErr:     "not permitted to spawn template",
		},
		{
			name:        "stop denied",
			permissions: `{"deny_tools":["leo_stop_agent"]}`,
			template:    "codex",
			wantErr:     "denies leo_stop_agent",
		},
		{
			name:        "spawn denied outright",
			permissions: `{"deny_tools":["leo_spawn_agent"]}`,
			template:    "codex",
			wantErr:     "denies leo_spawn_agent",
		},
		{
			name:        "malformed payload refuses",
			permissions: `{not json`,
			template:    "codex",
			wantErr:     "malformed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEO_PERMISSIONS", tc.permissions)
			err := gateTemplateSwitch("leo attach: set template", tc.template)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
