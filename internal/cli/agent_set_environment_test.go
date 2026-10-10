package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

func TestAgentSetEnvironmentRemote(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-environment", "leo-coding-bar", "base,acct-b", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent set-environment leo-coding-bar base,acct-b") || !strings.Contains(joined, "--json") {
		t.Errorf("unexpected call: %s", joined)
	}
}

// startSetEnvironmentDaemon answers resolve for an agent on template tmpl and
// captures the set-environment request body.
func startSetEnvironmentDaemon(t *testing.T, home, tmpl string, got *daemon.AgentSetEnvironmentRequest) {
	t.Helper()
	startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
		var data []byte
		switch {
		case r.URL.Path == "/agents/resolve":
			data, _ = json.Marshal(daemon.AgentResolveResponse{Name: "leo-x", Template: tmpl})
		case strings.HasSuffix(r.URL.Path, "/set-environment"):
			var buf bytes.Buffer
			buf.ReadFrom(r.Body) //nolint:errcheck
			_ = json.Unmarshal(buf.Bytes(), got)
			data, _ = json.Marshal(agent.SetEnvironmentsResult{Name: "leo-x", To: got.Environments, Effective: got.Environments, Status: "running"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(daemon.Response{OK: true, Data: data}) //nolint:errcheck
	})
}

func TestAgentSetEnvironmentLocalSendsOrderedNames(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	var got daemon.AgentSetEnvironmentRequest
	startSetEnvironmentDaemon(t, home, "coding", &got)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-environment", "x", "base,acct-b"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Environments) != 2 || got.Environments[0] != "base" || got.Environments[1] != "acct-b" {
		t.Fatalf("sent %v, want [base acct-b] in order", got.Environments)
	}
	if !strings.Contains(out.String(), "base, acct-b") {
		t.Errorf("output %q should state the new environments", out.String())
	}
}

func TestAgentSetEnvironmentWithoutListClearsOverride(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	var got daemon.AgentSetEnvironmentRequest
	startSetEnvironmentDaemon(t, home, "coding", &got)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "set-environment", "x"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Environments) != 0 {
		t.Fatalf("sent %v, want an empty list", got.Environments)
	}
	if !strings.Contains(out.String(), "template default") {
		t.Errorf("output %q should say the template default applies", out.String())
	}
}

func TestAgentSetEnvironmentHonorsPermissions(t *testing.T) {
	t.Run("denied tool", func(t *testing.T) {
		path := newAgentCLITestConfig(t)
		withStubExec(t)
		withStubStdio(t)
		t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_set_agent_environments"]}`)
		root := newRootCmd()
		root.SetArgs([]string{"--config", path, "agent", "set-environment", "leo-x", "acct-b"})
		if err := root.Execute(); err == nil {
			t.Fatal("expected a permission error when the template denies leo_set_agent_environments")
		}
	})

	t.Run("target template outside can_spawn", func(t *testing.T) {
		path, home := newAgentWorktreeTestConfig(t)
		withStubStdio(t)
		var got daemon.AgentSetEnvironmentRequest
		startSetEnvironmentDaemon(t, home, "coding", &got)
		t.Setenv("LEO_PERMISSIONS", `{"can_spawn":["review"]}`)
		root := newRootCmd()
		root.SetArgs([]string{"--config", path, "agent", "set-environment", "x", "acct-b"})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "coding") {
			t.Fatalf("err = %v, want a refusal naming the agent's template", err)
		}
		if got.Environments != nil {
			t.Fatal("the request must not reach the daemon")
		}
	})
}

func TestFormatSetEnvironmentsResult(t *testing.T) {
	cases := []struct {
		name string
		r    agent.SetEnvironmentsResult
		want []string
	}{
		{"running", agent.SetEnvironmentsResult{Name: "leo-x", To: []string{"acct-b"}, Effective: []string{"acct-b"}, Status: "running"}, []string{"leo-x", "acct-b", "restarted", "resumed"}},
		{"stopped", agent.SetEnvironmentsResult{Name: "leo-x", To: []string{"acct-b"}, Effective: []string{"acct-b"}, Status: "stopped"}, []string{"still stopped", "next start"}},
		{"cleared", agent.SetEnvironmentsResult{Name: "leo-x", Effective: []string{"base"}, Status: "running"}, []string{"template default", "base"}},
		{"unchanged", agent.SetEnvironmentsResult{Name: "leo-x", To: []string{"a"}, Effective: []string{"a"}, Status: "running", Unchanged: true}, []string{"nothing changed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatSetEnvironmentsResult(tc.r)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q missing %q", got, w)
				}
			}
		})
	}
}
