package cli

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentmove"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// moveDaemon is a stub daemon that resolves agent "x" to "leo-x" on
// template "coding", takes one move job and reports it through polls.
type moveDaemon struct {
	mu    sync.Mutex
	req   agentmove.Request
	polls []agentmove.JobStatus
	start func() (int, any)
	// settle answers /agents/leo-x/move/{reconcile,abandon}; settled
	// records which was asked.
	settle  func() (int, any)
	settled []string
}

func startMoveDaemon(t *testing.T, home string, d *moveDaemon) {
	t.Helper()
	pollMoveNow(t)
	startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/agents/resolve":
			data, _ := json.Marshal(daemon.AgentResolveResponse{Name: "leo-x", Template: "coding"})
			json.NewEncoder(w).Encode(daemon.Response{OK: true, Data: data}) //nolint:errcheck
		case r.Method == http.MethodPost && r.URL.Path == "/agents/leo-x/move":
			if err := json.NewDecoder(r.Body).Decode(&d.req); err != nil {
				t.Errorf("decoding the move request: %v", err)
			}
			status, body := http.StatusAccepted, any(agentmove.JobStatus{ID: "mv-1", State: agentmove.JobRunning})
			if d.start != nil {
				status, body = d.start()
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(body) //nolint:errcheck
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/agents/leo-x/move/"):
			d.settled = append(d.settled, strings.TrimPrefix(r.URL.Path, "/agents/leo-x/move/"))
			status, body := d.settle()
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(body) //nolint:errcheck
		case r.URL.Path == "/moves/mv-1":
			st := d.polls[0]
			if len(d.polls) > 1 {
				d.polls = d.polls[1:]
			}
			json.NewEncoder(w).Encode(st) //nolint:errcheck
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
}

// pollMoveNow makes job polls fire without waiting.
func pollMoveNow(t *testing.T) {
	t.Helper()
	old := movePollTick
	movePollTick = func() <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { movePollTick = old })
}

func stages(names ...string) []agentmove.StageEvent {
	out := make([]agentmove.StageEvent, len(names))
	for i, n := range names {
		out[i] = agentmove.StageEvent{Name: n}
	}
	return out
}

func TestAgentMoveFlagsToRequest(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	d := &moveDaemon{polls: []agentmove.JobStatus{
		{ID: "mv-1", State: agentmove.JobRunning, Stages: stages("validate", "preflight")},
		{ID: "mv-1", State: agentmove.JobDone, Stages: stages("validate", "preflight", "stop", "commit", "mark"),
			Notes:  []agentmove.Problem{{Code: agentmove.NotePersistentTaskWarned, Message: "backs task nightly"}},
			Result: &agentmove.Result{Name: "leo-x", Host: "mbp", To: "laptop", Workspace: "/w"}},
	}}
	startMoveDaemon(t, home, d)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--to", "laptop", "--force"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := (agentmove.Request{Mode: agentmove.ModeMove, To: "laptop", Force: true}); d.req != want {
		t.Fatalf("request = %+v, want %+v", d.req, want)
	}
	got := out.String()
	for _, want := range []string{"validate", "preflight", "commit", "backs task nightly", "moved leo-x to laptop", "leo --host laptop agent attach leo-x"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "preflight") != 1 {
		t.Errorf("a stage printed twice:\n%s", got)
	}
}

func TestAgentMoveNeedsTo(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("move without --to = %v", err)
	}
}

func TestAgentMoveFailureExitsNonzero(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	withStubStdio(t)
	d := &moveDaemon{polls: []agentmove.JobStatus{{ID: "mv-1", State: agentmove.JobFailed, Stages: stages("validate", "preflight"),
		Error:    &agentmove.Problem{Code: agentmove.CodeTemplateMissing, Message: "template_missing: no template; environment_missing: no env"},
		Problems: []agentmove.Problem{{Code: agentmove.CodeTemplateMissing, Message: "no template"}, {Code: agentmove.CodeEnvironmentMissing, Message: "no env"}}}}}
	startMoveDaemon(t, home, d)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--to", "laptop"})
	root.SilenceErrors, root.SilenceUsage = true, true
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no template") || !strings.Contains(err.Error(), "no env") {
		t.Fatalf("failed move = %v, want every problem", err)
	}
}

func TestAgentMoveRefusedAtStart(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	withStubStdio(t)
	d := &moveDaemon{start: func() (int, any) {
		return http.StatusConflict, agentmove.VerbError{Code: agentmove.CodeAgentMoving, Message: "leo-x is already being transferred"}
	}}
	startMoveDaemon(t, home, d)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--to", "laptop"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "already being transferred") {
		t.Fatalf("refused move = %v", err)
	}
}

func TestAgentCopyJSONOutput(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	final := agentmove.JobStatus{ID: "mv-1", State: agentmove.JobDone, Stages: stages("validate", "commit"),
		Result: &agentmove.Result{Name: "y", Host: "mbp", Workspace: "/w-y", Branch: "b"}}
	d := &moveDaemon{polls: []agentmove.JobStatus{{ID: "mv-1", State: agentmove.JobRunning}, final}}
	startMoveDaemon(t, home, d)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "copy", "x", "--name", "y", "--branch", "b", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := (agentmove.Request{Mode: agentmove.ModeCopy, Name: "y", Branch: "b"}); d.req != want {
		t.Fatalf("request = %+v, want %+v", d.req, want)
	}
	var got agentmove.JobStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not one job status: %v\n%s", err, out)
	}
	if got.State != agentmove.JobDone || got.Result == nil || got.Result.Name != "y" || got.Result.Branch != "b" {
		t.Fatalf("json = %+v", got)
	}
}

func TestAgentCopyGatedBySpawnTemplate(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	withStubStdio(t)
	startMoveDaemon(t, home, &moveDaemon{})
	t.Setenv("LEO_PERMISSIONS", `{"can_spawn":["review"]}`)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "copy", "x"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("copy of a template outside can_spawn = %v", err)
	}
}

func TestAgentMoveRemoteForwards(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--to", "laptop", "--force", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent move x --to laptop --force --json") {
		t.Fatalf("remote call = %s", joined)
	}
}

func TestAgentCopyRemoteForwardsInOrder(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "copy", "x", "--branch", "b", "--name", "y", "--to", "laptop"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if joined := strings.Join(stub.calls[0], " "); !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent copy x --to laptop --name y --branch b") {
		t.Fatalf("remote call = %s", joined)
	}
}

func TestAgentMoveGatedByItsTool(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	withStubStdio(t)
	startMoveDaemon(t, home, &moveDaemon{})
	for _, c := range []struct{ verb, tool string }{{"move", "leo_agent_move"}, {"copy", "leo_agent_copy"}, {"copy", "leo_spawn_agent"}} {
		t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["`+c.tool+`"]}`)
		root := newRootCmd()
		root.SetArgs([]string{"--config", path, "agent", c.verb, "x", "--to", "laptop"})
		root.SilenceErrors, root.SilenceUsage = true, true
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), c.tool) {
			t.Errorf("%s with %s denied = %v", c.verb, c.tool, err)
		}
	}
}

// remoteResolveStub answers the remote `agent session-name <q> --json` with
// an agent of template tmpl, and every other ssh call with success.
func remoteResolveStub(t *testing.T, tmpl string) *[][]string {
	t.Helper()
	var calls [][]string
	old := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if slices.Contains(args, "session-name") && slices.Contains(args, "--json") {
			return exec.Command("printf", "%s", `{"name":"leo-x","session":"leo-x","template":"`+tmpl+`"}`)
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { agentExecCommand = old })
	oldTI := ensureRemoteTerminfoFn
	ensureRemoteTerminfoFn = func(config.HostResolution) string { return "" }
	t.Cleanup(func() { ensureRemoteTerminfoFn = oldTI })
	return &calls
}

func forwardedCopy(calls [][]string) bool {
	return slices.ContainsFunc(calls, func(c []string) bool { return strings.Contains(strings.Join(c, " "), " agent copy x") })
}

// ssh does not carry LEO_PERMISSIONS, so a copy forwarded to another host is
// gated here, against the remote agent's template, before anything is sent.
func TestAgentCopyRemoteGatedBySpawnTemplate(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)
	for _, c := range []struct {
		perms   string
		allowed bool
	}{
		{`{"can_spawn":["review"]}`, false},
		{`{"deny_tools":["leo_spawn_agent"]}`, false},
		{`{"can_spawn":["cod*"]}`, true},
	} {
		calls := remoteResolveStub(t, "coding")
		t.Setenv("LEO_PERMISSIONS", c.perms)
		root := newRootCmd()
		root.SetArgs([]string{"--config", path, "agent", "copy", "x", "--to", "laptop"})
		root.SilenceErrors, root.SilenceUsage = true, true
		err := root.Execute()
		if c.allowed != (err == nil) || c.allowed != forwardedCopy(*calls) {
			t.Errorf("%s: err = %v, forwarded = %v; want allowed %v", c.perms, err, forwardedCopy(*calls), c.allowed)
		}
	}
}

func TestAgentSessionNameJSON(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	startMoveDaemon(t, home, &moveDaemon{})
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "session-name", "x", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var got daemon.AgentResolveResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Name != "leo-x" || got.Template != "coding" {
		t.Fatalf("session-name --json = %s (%v)", out, err)
	}
}

func TestAgentMoveReconcile(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	d := &moveDaemon{settle: func() (int, any) {
		return http.StatusOK, agentmove.SettleResult{Name: "leo-x", To: "laptop", RunID: "run-1", Outcome: agentmove.SettleFailed}
	}}
	startMoveDaemon(t, home, d)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--reconcile"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !slices.Equal(d.settled, []string{"reconcile"}) {
		t.Fatalf("settled = %v", d.settled)
	}
	if got := out.String(); !strings.Contains(got, "failed on laptop") || !strings.Contains(got, "leo agent start leo-x") {
		t.Fatalf("output = %q", got)
	}

	d.settle = func() (int, any) {
		return http.StatusConflict, agentmove.VerbError{Code: agentmove.CodeOutcomeUnknown, Message: "the move of leo-x to laptop has not settled there"}
	}
	root = newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--reconcile"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "has not settled") {
		t.Fatalf("unsettled reconcile = %v", err)
	}

	root = newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--reconcile", "--to", "laptop"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--reconcile") {
		t.Fatalf("--reconcile with --to = %v, want a refusal", err)
	}
}

// --abandon-remote lifts the fence only after a confirmation or --yes, and
// says what it risks.
func TestAgentMoveAbandonRemote(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	d := &moveDaemon{settle: func() (int, any) {
		return http.StatusOK, agentmove.SettleResult{Name: "leo-x", To: "laptop", RunID: "run-1", Outcome: agentmove.SettleAbandoned}
	}}
	startMoveDaemon(t, home, d)
	oldTTY, oldIn := agentIsTTY, agentStdin
	t.Cleanup(func() { agentIsTTY, agentStdin = oldTTY, oldIn })
	run := func(args ...string) error {
		root := newRootCmd()
		root.SetArgs(append([]string{"--config", path, "agent", "move", "x"}, args...))
		root.SilenceErrors, root.SilenceUsage = true, true
		return root.Execute()
	}

	agentIsTTY = func() bool { return false }
	if err := run("--abandon-remote"); err == nil || !strings.Contains(err.Error(), "--yes") || len(d.settled) != 0 {
		t.Fatalf("unconfirmed abandon without a TTY = %v (settled %v), want a refusal naming --yes", err, d.settled)
	}
	agentIsTTY = func() bool { return true }
	agentStdin = strings.NewReader("n\n")
	if err := run("--abandon-remote"); err != nil || len(d.settled) != 0 || !strings.Contains(out.String(), "Aborted") {
		t.Fatalf("declined abandon = %v (settled %v, output %q)", err, d.settled, out)
	}
	if !strings.Contains(out.String(), "two live copies") {
		t.Fatalf("the prompt does not warn of two live copies: %q", out)
	}
	agentStdin = strings.NewReader("y\n")
	if err := run("--abandon-remote"); err != nil || !slices.Equal(d.settled, []string{"abandon"}) {
		t.Fatalf("confirmed abandon = %v (settled %v)", err, d.settled)
	}
	agentIsTTY = func() bool { return false }
	out.Reset()
	if err := run("--abandon-remote", "--yes"); err != nil || len(d.settled) != 2 {
		t.Fatalf("abandon --yes = %v (settled %v)", err, d.settled)
	}
	if got := out.String(); !strings.Contains(got, "laptop") || !strings.Contains(got, "two live copies") {
		t.Fatalf("output = %q, want the lifted fence and its risk", got)
	}
	if err := run("--abandon-remote", "--reconcile"); err == nil || len(d.settled) != 2 {
		t.Fatalf("--abandon-remote with --reconcile = %v, want a refusal", err)
	}
}

func TestAgentMovePrintsRsyncSummaryPerStage(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)
	final := agentmove.JobStatus{ID: "mv-1", State: agentmove.JobDone, Stages: stages("validate", "commit"),
		Result: &agentmove.Result{Name: "x", Host: "mbp", To: "laptop", Rsync: []agentmove.RsyncStage{
			{Stage: "rsync-workspace", Total: 3, Lines: []string{"a", "b", "c"}},
			{Stage: "rsync-sessions", Total: 250, Lines: make([]string, 200), Truncated: 50},
			{Stage: "rsync-staging", Lines: []string{}},
		}}}
	startMoveDaemon(t, home, &moveDaemon{polls: []agentmove.JobStatus{final}})
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "move", "x", "--to", "laptop"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"rsync-workspace: 3 changes", "rsync-sessions: 250 changes", "rsync-staging: no changes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
