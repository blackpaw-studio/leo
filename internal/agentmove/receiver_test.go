package agentmove

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
	"github.com/blackpaw-studio/leo/internal/session"
)

type fakeAgents struct {
	list, afterReceive []agent.Record
	received, shadows  []agent.ReceiveSpec
	receiveErr         error
}

func (f *fakeAgents) List() []agent.Record { return f.list }
func (f *fakeAgents) Receive(_ context.Context, spec agent.ReceiveSpec) error {
	f.received = append(f.received, spec)
	if f.receiveErr != nil {
		return f.receiveErr
	}
	f.list = f.afterReceive
	return nil
}
func (f *fakeAgents) ShadowReceive(spec agent.ReceiveSpec, _ error) error {
	f.shadows = append(f.shadows, spec)
	return nil
}

type hookCall struct {
	phase string
	paths []string
	env   HookEnv
}

type fakeHooks struct {
	calls []hookCall
	err   error
}

func (f *fakeHooks) RunHooks(_ context.Context, phase string, paths []string, _ time.Duration, env HookEnv) error {
	f.calls = append(f.calls, hookCall{phase, paths, env})
	return f.err
}

type instantClock struct{ waited *[]time.Duration }

func (instantClock) Now() time.Time { return receiveTime }
func (c instantClock) After(d time.Duration) <-chan time.Time {
	*c.waited = append(*c.waited, d)
	ch := make(chan time.Time, 1)
	ch <- receiveTime
	return ch
}

var receiveTime = time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)

type receiverFixture struct {
	r       *Receiver
	agents  *fakeAgents
	hooks   *fakeHooks
	waited  []time.Duration
	home    string
	claude  string
	ws      string
	running []agent.Record
}

func newReceiverFixture(t *testing.T) *receiverFixture {
	t.Helper()
	f := &receiverFixture{home: t.TempDir(), claude: t.TempDir(), agents: &fakeAgents{}, hooks: &fakeHooks{}}
	f.ws = filepath.Join(t.TempDir(), "ws")
	cfg := &config.Config{
		HomePath:     f.home,
		Environments: map[string]map[string]string{"work": {"CLAUDE_CONFIG_DIR": f.claude}},
		Templates: map[string]config.TemplateConfig{"coder": {
			Environments: []string{"work"},
			Move:         config.MoveConfig{Hooks: config.MoveHooksConfig{PostReceive: []string{"/bin/import"}}},
		}},
	}
	f.agents.afterReceive = []agent.Record{{Name: "web", Status: "running"}}
	f.r = &Receiver{
		Config: func() (*config.Config, error) { return cfg, nil }, Agents: f.agents, Hooks: f.hooks,
		Runner: &fakeRunner{}, Clock: instantClock{&f.waited}, Locks: &KeyedLocks{},
		Host:     func() (Host, error) { return Host{Hostname: "laptop", Home: "/Users/me", User: "me"}, nil },
		LookPath: func(name string) (string, error) { return "/usr/local/bin/" + name, nil },
		Version:  "v1", StartConfirm: DefaultStartConfirm,
	}
	return f
}

func (f *receiverFixture) manifest() Manifest {
	return Manifest{
		Protocol: Protocol, Mode: ModeMove, From: studio(), SourceSessionID: "final-sid",
		Agent: agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: f.ws, SessionID: "stale-sid"},
	}
}

func (f *receiverFixture) call(t *testing.T, verb string, m Manifest, out any) error {
	t.Helper()
	body, _ := json.Marshal(m)
	data, err := f.r.Handle(context.Background(), verb, body)
	if err == nil && out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decoding %s: %v (%s)", verb, err, data)
		}
	}
	return err
}

func TestReceiverPreflightGathersFacts(t *testing.T) {
	f := newReceiverFixture(t)
	f.agents.list = []agent.Record{{Name: "api", Workspace: f.ws}}
	var resp PreflightResponse
	if err := f.call(t, VerbPreflight, f.manifest(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || codes(resp.Problems) != CodeWorkspaceInUse {
		t.Fatalf("problems = %+v, want workspace_in_use", resp.Problems)
	}
	if resp.SessionRoot != f.claude || resp.StagingDir != filepath.Join(f.home, "state/moves/web/staging") || resp.WSState != WSAbsent || resp.Host.Hostname != "laptop" || resp.LeoVersion != "v1" {
		t.Fatalf("response = %+v", resp)
	}

	f.agents.list = []agent.Record{{Name: "web", MovedTo: "studio", Workspace: f.ws}}
	gittest.Write(t, filepath.Join(f.ws, "kept.txt"), "x\n")
	resp = PreflightResponse{}
	if err := f.call(t, VerbPreflight, f.manifest(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.WSState != WSOurs || !resp.Existing.Present || resp.Existing.MovedTo != "studio" {
		t.Fatalf("move back onto a shadow = %+v", resp)
	}

	if err := os.RemoveAll(f.claude); err != nil {
		t.Fatal(err)
	}
	resp = PreflightResponse{}
	_ = f.call(t, VerbPreflight, f.manifest(), &resp)
	if codes(resp.Problems) != CodeSessionRootMissing {
		t.Fatalf("problems = %+v, want session_root_missing", resp.Problems)
	}
}

func TestReceiverCommitRunsStagesInOrder(t *testing.T) {
	f := newReceiverFixture(t)
	var res CommitResult
	if err := f.call(t, VerbCommit, f.manifest(), &res); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Name != "web" || res.Status != "running" {
		t.Fatalf("result = %+v", res)
	}
	if len(f.hooks.calls) != 1 || f.hooks.calls[0].phase != "post_receive" || f.hooks.calls[0].paths[0] != "/bin/import" {
		t.Fatalf("hooks = %+v", f.hooks.calls)
	}
	he := f.hooks.calls[0].env
	if he.From != "studio" || he.To != "laptop" || he.SessionID != "final-sid" || he.Dir != filepath.Join(f.home, "state/moves/web/staging/hooks") || he.AgentEnv["CLAUDE_CONFIG_DIR"] != f.claude {
		t.Fatalf("hook env = %+v", he)
	}
	if len(f.agents.received) != 1 {
		t.Fatalf("received = %+v", f.agents.received)
	}
	spec := f.agents.received[0]
	if spec.Record.SessionID != "final-sid" || spec.From != "studio" || spec.Copy || !spec.At.Equal(receiveTime) {
		t.Fatalf("spec = %+v", spec)
	}
	if len(f.waited) != 1 || f.waited[0] != DefaultStartConfirm {
		t.Fatalf("start confirmation waits = %v", f.waited)
	}
	if len(f.agents.shadows) != 0 {
		t.Fatalf("a successful commit left a shadow: %+v", f.agents.shadows)
	}
}

func TestReceiverCommitFailuresLeaveShadow(t *testing.T) {
	f := newReceiverFixture(t)
	f.hooks.err = errors.New("post_receive hook /bin/import exited with status 1")
	if err := f.call(t, VerbCommit, f.manifest(), nil); err == nil || !strings.Contains(err.Error(), "/bin/import") {
		t.Fatalf("commit = %v, want the hook failure", err)
	}
	if len(f.agents.received) != 0 || len(f.agents.shadows) != 1 || f.agents.shadows[0].From != "studio" {
		t.Fatalf("received %d, shadows %+v", len(f.agents.received), f.agents.shadows)
	}

	q := newReceiverFixture(t)
	q.agents.afterReceive = []agent.Record{{Name: "web", Status: "running", Restarts: 2}}
	if err := q.call(t, VerbCommit, q.manifest(), nil); err == nil || !strings.Contains(err.Error(), "did not stay up") {
		t.Fatalf("quick exit = %v", err)
	}
	if len(q.agents.shadows) != 1 {
		t.Fatalf("a quick exit left no shadow: %+v", q.agents.shadows)
	}
}

func TestReceiverStatus(t *testing.T) {
	f := newReceiverFixture(t)
	f.agents.list = []agent.Record{{Name: "web", Status: "running"}}
	var st StatusResult
	if err := f.call(t, VerbStatus, f.manifest(), &st); err != nil || !st.Present || !st.Live {
		t.Fatalf("status = %+v, %v", st, err)
	}
	f.agents.list = nil
	st = StatusResult{}
	if err := f.call(t, VerbStatus, f.manifest(), &st); err != nil || st.Present {
		t.Fatalf("absent status = %+v, %v", st, err)
	}
}

func TestReceiverRejectsBadRequests(t *testing.T) {
	f := newReceiverFixture(t)
	m := f.manifest()
	m.Agent.Workspace = "/Users/me/my agent"
	if err := f.call(t, VerbCommit, m, nil); !HasCode(err, CodeUnsafePath) {
		t.Errorf("unsafe workspace = %v", err)
	}
	if err := f.call(t, "rm-rf", f.manifest(), nil); !HasCode(err, CodeBadRequest) {
		t.Errorf("unknown verb = %v", err)
	}
	if _, err := f.r.Handle(context.Background(), VerbCommit, []byte("{")); !HasCode(err, CodeBadRequest) {
		t.Errorf("bad json = %v", err)
	}
	stale := f.manifest()
	stale.Protocol = 0
	if err := f.call(t, VerbGitPrepare, stale, nil); !HasCode(err, CodeProtocolMismatch) {
		t.Errorf("old protocol = %v", err)
	}
	if len(f.agents.received)+len(f.agents.shadows)+len(f.hooks.calls) != 0 {
		t.Error("a refused request reached the agent")
	}
}

func TestReceiverPlainCopyPlacesPartial(t *testing.T) {
	f := newReceiverFixture(t)
	src := gittest.NewRepo(t)
	copyWS := filepath.Join(filepath.Dir(f.ws), "ws-copy")
	if err := os.Rename(src, copyWS+PartialSuffix); err != nil {
		t.Fatal(err)
	}
	stage := StagingDir(f.home, "web-copy")
	gittest.Write(t, filepath.Join(stage, "harness/claude/final-sid.jsonl"), `{"type":"assistant","message":{"stop_reason":"end_turn"}}`+"\n")
	f.agents.afterReceive = []agent.Record{{Name: "web-copy", Status: "running"}}
	m := f.manifest()
	m.Mode, m.Copy = ModeCopy, &CopySpec{Name: "web-copy", Workspace: copyWS, Branch: "main-web-copy", SessionID: "fork-sid"}
	if err := f.call(t, VerbCommit, m, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(copyWS + PartialSuffix); !os.IsNotExist(err) {
		t.Errorf("partial still there: %v", err)
	}
	if got := gittest.Run(t, copyWS, "symbolic-ref", "HEAD"); got != "refs/heads/main-web-copy" {
		t.Errorf("copy HEAD = %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.claude, "projects", session.ProjectSlug(copyWS), "final-sid.jsonl")); err != nil {
		t.Errorf("the staged transcript was not placed for the copy: %v", err)
	}
	spec := f.agents.received[0]
	if !spec.Copy || spec.ForkFrom != "final-sid" || spec.Record.Name != "web-copy" || spec.Record.Workspace != copyWS || spec.Record.SessionID != "fork-sid" || spec.Record.Branch != "" {
		t.Fatalf("spec = %+v", spec)
	}
	if he := f.hooks.calls[0].env; he.Agent != "web-copy" || he.Workspace != copyWS || he.SessionID != "fork-sid" || he.SourceAgent != "web" || he.Mode != ModeCopy {
		t.Fatalf("copy hook env = %+v", he)
	}
}

// GNU rsync creates only a target's last path component, so an accepted
// preflight makes the staging dir and the workspace's parent; a refused one
// touches nothing.
func TestReceiverPreflightMakesTargetParents(t *testing.T) {
	f := newReceiverFixture(t)
	m := f.manifest()
	m.Agent.Workspace = filepath.Join(t.TempDir(), "deep", "er", "web")
	stage := filepath.Join(f.home, "state/moves/web/staging")
	f.agents.list = []agent.Record{{Name: "web", Status: "running"}}
	var resp PreflightResponse
	if err := f.call(t, VerbPreflight, m, &resp); err != nil || resp.OK {
		t.Fatalf("preflight = %+v %v, want name_taken", resp, err)
	}
	if isDir(stage) || isDir(filepath.Dir(m.Agent.Workspace)) {
		t.Fatal("a refused preflight created dirs")
	}
	f.agents.list = nil
	if err := f.call(t, VerbPreflight, m, &resp); err != nil || !resp.OK {
		t.Fatalf("preflight = %+v %v", resp, err)
	}
	if !isDir(stage) || !isDir(filepath.Dir(m.Agent.Workspace)) || isDir(m.Agent.Workspace) {
		t.Fatal("accepted preflight did not make the staging dir and the workspace parent (only)")
	}
}
