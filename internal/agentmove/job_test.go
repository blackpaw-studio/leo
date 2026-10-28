package agentmove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/git/gittest"
	"github.com/blackpaw-studio/leo/internal/session"
)

// eventLog is one ordered record of what a job did across every fake.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) has(prefix string) bool {
	for _, e := range l.all() {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

type fakeSource struct {
	// home holds the agentstore the fence is kept in, as agent.Manager
	// keeps it.
	home    string
	envs    *configEnvs
	log     *eventLog
	mu      sync.Mutex
	held    map[string]agent.MoveMode
	stopErr error
	// markFails fails that many MarkMoved calls before one succeeds.
	markFails int
}

func (f *fakeSource) RuntimeEnv(rec agentstore.Record) (map[string]string, error) {
	return f.envs.of(rec)
}

func (f *fakeSource) BeginMove(name string, mode agent.MoveMode) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held == nil {
		f.held = map[string]agent.MoveMode{}
	}
	if _, ok := f.held[name]; ok {
		return nil, fmt.Errorf("%w: %q", agent.ErrAgentMoving, name)
	}
	f.held[name] = mode
	f.log.add("lock %s %s", name, mode)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.held[name]; ok {
			delete(f.held, name)
			f.log.add("unlock %s", name)
		}
	}, nil
}

func (f *fakeSource) locked(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.held[name]
	return ok
}

func (f *fakeSource) Stop(name string, opts agent.StopOptions) error {
	f.log.add("stop %s wake=%v", name, opts.WakeOnMessage)
	return f.stopErr
}

func (f *fakeSource) MarkMoved(name, to, peer string, _ time.Time) error {
	f.log.add("mark %s %s %s", name, to, peer)
	if f.markFails > 0 {
		f.markFails--
		return errors.New("store busy")
	}
	return f.setFence(name, nil)
}

func (f *fakeSource) FenceMove(name string, p agentstore.MovePending) error {
	f.log.add("fence %s %s", name, p.To)
	return f.setFence(name, &p)
}

func (f *fakeSource) ClearMoveFence(name, runID string) error {
	f.log.add("unfence %s", name)
	recs, err := agentstore.Load(agentstore.FilePath(f.home))
	if err != nil {
		return err
	}
	if p := recs[name].MovePending; p == nil || p.RunID != runID {
		return nil
	}
	return f.setFence(name, nil)
}

func (f *fakeSource) setFence(name string, p *agentstore.MovePending) error {
	return agentstore.Update(f.home, name, func(r agentstore.Record) agentstore.Record {
		r.MovePending = p
		return r
	})
}

// fence is name's stored fence, nil when none.
func (f *fakeSource) fence(t *testing.T, name string) *agentstore.MovePending {
	t.Helper()
	recs, err := agentstore.Load(agentstore.FilePath(f.home))
	if err != nil {
		t.Fatal(err)
	}
	return recs[name].MovePending
}

// idleSupervisor is an agent.Supervisor with nothing live, for a real
// agent.Manager whose launches must be refused before they reach it.
type idleSupervisor struct{ agent.Supervisor }

func (idleSupervisor) EphemeralAgents() map[string]agent.ProcessState { return nil }

// fakeEndpoint is a remote destination whose verbs answer from a script.
type fakeEndpoint struct {
	log       *eventLog
	local     bool
	answer    map[string]func(Manifest) ([]byte, error)
	manifests []Manifest
}

func (e *fakeEndpoint) Run(_ context.Context, verb string, stdin []byte) ([]byte, error) {
	var m Manifest
	if err := json.Unmarshal(stdin, &m); err != nil {
		return nil, err
	}
	e.manifests = append(e.manifests, m)
	e.log.add("verb %s", verb)
	if a := e.answer[verb]; a != nil {
		return a(m)
	}
	return []byte("{}"), nil
}
func (e *fakeEndpoint) Local() bool        { return e.local }
func (e *fakeEndpoint) RsyncShell() string { return "ssh -o BatchMode=yes" }
func (e *fakeEndpoint) Target(p string) string {
	return "u@laptop:" + p
}
func (e *fakeEndpoint) Name() string { return "laptop" }
func (e *fakeEndpoint) Push(ws, canonical, gitPath string, refspecs []string) (Cmd, bool, error) {
	if e.local {
		return Cmd{}, false, nil
	}
	return Cmd{Name: "git", Args: append([]string{"-C", ws, "push", "u@laptop:" + canonical}, refspecs...)}, true, nil
}

// runLine is c as the event log shows it, with its working dir.
func runLine(c Cmd) string {
	if c.Dir != "" {
		return c.String() + " (in " + c.Dir + ")"
	}
	return c.String()
}

// mirrorSessionTargets answers session-targets with each rel's dir under root.
func mirrorSessionTargets(root string) func(Manifest) ([]byte, error) {
	return func(m Manifest) ([]byte, error) {
		var out SessionTargetsResponse
		for _, rel := range m.SessionRels {
			dir := filepath.Join(root, rel)
			if !strings.HasSuffix(rel, "/") {
				dir = filepath.Dir(dir)
			}
			out.Targets = append(out.Targets, dir)
		}
		return json.Marshal(out)
	}
}

func jsonAnswer(v any) func(Manifest) ([]byte, error) {
	return func(Manifest) ([]byte, error) { return json.Marshal(v) }
}

type loggingHooks struct {
	log *eventLog
	err error
	env []HookEnv
}

func (h *loggingHooks) RunHooks(_ context.Context, phase string, paths []string, _ time.Duration, env HookEnv) error {
	h.log.add("hooks %s %s", phase, strings.Join(paths, ","))
	h.env = append(h.env, env)
	return h.err
}

type fakeProber struct {
	probe drain.Probe
	err   error
	hook  func()
}

func (p *fakeProber) Probe(_ context.Context, name string) (drain.Probe, error) {
	if p.hook != nil {
		p.hook()
	}
	pr := p.probe
	pr.Name = name
	return pr, p.err
}

type moveFixture struct {
	// destHooks are a local copy's destination hooks (localCopy).
	destHooks *fakeHooks
	mv        *Mover
	log       *eventLog
	src       *fakeSource
	ep        *fakeEndpoint
	runner    *fakeRunner
	hooks     *loggingHooks
	prober    *fakeProber
	cfg       *config.Config
	home      string
	ws        string
	claude    string
	rec       agentstore.Record
}

const destStage = "/Users/me/.leo/state/moves/web/staging"

func laptopHost() Host { return Host{Hostname: "laptop", Home: "/Users/me", User: "me"} }

// newMoveFixture is a claude agent "web" on a plain workspace with one
// transcript, moving to a scripted destination that accepts everything.
func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	log := &eventLog{}
	f := &moveFixture{
		log: log, src: &fakeSource{log: log, envs: &configEnvs{}}, hooks: &loggingHooks{log: log}, prober: &fakeProber{probe: drain.Probe{Exists: true}},
		home: t.TempDir(), claude: t.TempDir(), ws: filepath.Join(t.TempDir(), "web"),
	}
	f.src.home = f.home
	f.runner = &fakeRunner{answer: func(c Cmd) ([]byte, error) {
		log.add("run %s", runLine(c))
		return nil, nil
	}}
	if err := os.MkdirAll(f.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.ws, "notes.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(f.claude, "projects", session.ProjectSlug(f.ws))
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "sid-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.cfg = &config.Config{
		HomePath:     f.home,
		Environments: map[string]config.EnvMap{"work": config.LiteralEnv(map[string]string{"CLAUDE_CONFIG_DIR": f.claude})},
		Templates: map[string]config.TemplateConfig{"coder": {
			Environments: []string{"work"},
			Move:         config.MoveConfig{Exclude: []string{"node_modules/"}, Hooks: config.MoveHooksConfig{PreSend: []string{"/bin/export"}}},
		}},
	}
	f.src.envs.cfg = f.cfg
	f.rec = agentstore.Record{Name: "web", Template: "coder", Harness: "claude", Workspace: f.ws, SessionID: "sid-1", Environments: []string{"work"},
		MoveLog: map[string]time.Time{"laptop": receiveTime.Add(-time.Hour)}}
	if err := agentstore.Save(f.home, f.rec); err != nil {
		t.Fatal(err)
	}
	f.ep = &fakeEndpoint{log: log, answer: map[string]func(Manifest) ([]byte, error){
		VerbPreflight: jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true,
			SessionRoot: "/Users/me/.claude-work", StagingDir: destStage, WSState: WSAbsent}),
		VerbSessionTargets: mirrorSessionTargets("/Users/me/.claude-work"),
		VerbCommit:         jsonAnswer(CommitResult{Name: "web", Status: "running"}),
		VerbStatus:         jsonAnswer(StatusResult{Present: true, Status: "running", Live: true}),
	}}
	var waited []time.Duration
	f.mv = &Mover{
		Config:   func() (*config.Config, error) { return f.cfg, nil },
		Agents:   f.src,
		Endpoint: func(*config.Config, string) (Endpoint, error) { return f.ep, nil },
		Runner:   f.runner, Hooks: f.hooks, Prober: f.prober, Clock: instantClock{&waited},
		Host: func() (Host, error) { return studio(), nil },
	}
	return f
}

func (f *moveFixture) move(t *testing.T, force bool) (Result, error) {
	t.Helper()
	return f.mv.Run(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop", Force: force}, nil)
}

func TestMoveHappyPathOrder(t *testing.T) {
	f := newMoveFixture(t)
	res, err := f.move(t, false)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	stage := StagingDir(f.home, "web")
	realClaude, _ := filepath.EvalSymlinks(f.claude)
	want := []string{
		"lock web move",
		"verb preflight",
		"stop web wake=false",
		"hooks pre_send /bin/export",
		"run rsync -a --delete --itemize-changes --stats -e ssh -o BatchMode=yes --exclude=node_modules/ --exclude=/.worktrees/ " + f.ws + "/ u@laptop:" + f.ws + "/",
		"verb session-targets",
		"run rsync -a -u --itemize-changes -e ssh -o BatchMode=yes " + realClaude + "/projects/" + session.ProjectSlug(f.ws) + "/ u@laptop:/Users/me/.claude-work/projects/" + session.ProjectSlug(f.ws) + "/",
		"run rsync -a --delete --itemize-changes -e ssh -o BatchMode=yes " + stage + "/ u@laptop:" + destStage + "/",
		"fence web laptop",
		"verb commit",
		"mark web laptop laptop",
		"unlock web",
	}
	if got := f.log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	commit := f.ep.manifests[1]
	if commit.SourceSessionID != "sid-1" || commit.Mode != ModeMove || commit.Agent.Name != "web" || commit.From != studio() {
		t.Fatalf("commit manifest = %+v", commit)
	}
	if commit.LastMove == nil || !commit.LastMove.Equal(receiveTime.Add(-time.Hour)) {
		t.Fatalf("LastMove = %v, want the previous studio→laptop move", commit.LastMove)
	}
	he := f.hooks.env[0]
	if he.From != "studio" || he.To != "laptop" || he.Dir != filepath.Join(stage, "hooks") || he.SessionID != "sid-1" || he.AgentEnv["CLAUDE_CONFIG_DIR"] != f.claude {
		t.Fatalf("pre_send env = %+v", he)
	}
	if res.Name != "web" || res.Host != "laptop" || res.To != "laptop" || res.Workspace != f.ws {
		t.Fatalf("result = %+v", res)
	}
}

// After the stop, every failure leaves the source stopped and unmarked, says
// how to recover, and releases the lock; before it, nothing is touched.
func TestMoveFailureLeavesSourceRecoverable(t *testing.T) {
	refuse := func(code string) func(Manifest) ([]byte, error) {
		return func(Manifest) ([]byte, error) { return nil, &VerbError{Code: code, Message: "no"} }
	}
	cases := []struct {
		name      string
		arrange   func(f *moveFixture)
		code      string
		afterStop bool
	}{
		{"preflight", func(f *moveFixture) {
			f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), Problems: []Problem{{Code: CodeTemplateMissing, Message: "no template"}}})
		}, CodeTemplateMissing, false},
		{"busy", func(f *moveFixture) { f.prober.probe.Busy = true }, CodeAgentBusy, false},
		{"stop", func(f *moveFixture) { f.src.stopErr = errors.New("tmux gone") }, CodeMoveFailed, false},
		{"presend", func(f *moveFixture) { f.hooks.err = errors.New("hook exited 3") }, CodeMoveFailed, true},
		{"rsync", func(f *moveFixture) {
			f.runner.answer = func(c Cmd) ([]byte, error) {
				f.log.add("run %s", runLine(c))
				if strings.Contains(c.String(), "--stats") {
					return nil, errors.New("rsync: exit 23")
				}
				return nil, nil
			}
		}, CodeMoveFailed, true},
		{"staging", func(f *moveFixture) {
			f.runner.answer = func(c Cmd) ([]byte, error) {
				if strings.Contains(c.String(), destStage) {
					return nil, errors.New("rsync: exit 12")
				}
				return nil, nil
			}
		}, CodeMoveFailed, true},
		{"commit", func(f *moveFixture) { rolledBack(f, refuse(CodeHarnessBinaryMissing)) }, CodeHarnessBinaryMissing, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t)
			tc.arrange(f)
			_, err := f.move(t, false)
			if err == nil {
				t.Fatal("move succeeded")
			}
			if got := CodeOf(err); got != tc.code {
				t.Errorf("code = %s, want %s (%v)", got, tc.code, err)
			}
			if f.log.has("mark ") {
				t.Error("source marked moved after a failure")
			}
			if f.src.locked("web") {
				t.Error("move lock still held")
			}
			recovers := strings.Contains(err.Error(), "leo agent start web")
			if recovers != tc.afterStop {
				t.Errorf("recovery text = %v, want %v: %v", recovers, tc.afterStop, err)
			}
			if !tc.afterStop && f.log.has("stop ") && tc.name != "stop" {
				t.Error("source stopped by a refusal before the stop stage")
			}
		})
	}
}

func TestMoveBusyRefusedWithoutForce(t *testing.T) {
	f := newMoveFixture(t)
	f.prober.probe.Busy = true
	if _, err := f.move(t, false); !HasCode(err, CodeAgentBusy) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("busy move = %v, want agent_busy naming --force", err)
	}
	if f.log.has("stop ") {
		t.Fatal("busy agent stopped")
	}
	f.prober.probe.Busy, f.prober.err = false, errors.New("capture-pane failed")
	if _, err := f.move(t, false); !HasCode(err, CodeAgentBusy) {
		t.Fatalf("unknown busy state = %v, want agent_busy", err)
	}
	f.prober.probe.Busy, f.prober.err = true, nil
	if _, err := f.move(t, true); err != nil {
		t.Fatalf("forced move: %v", err)
	}
	if !f.log.has("mark web") {
		t.Fatal("forced move did not finish")
	}
}

func TestMoveUnknownCommitQueriesStatus(t *testing.T) {
	lost := func(Manifest) ([]byte, error) { return nil, errors.New("ssh: exit status 255") }
	t.Run("live", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.answer[VerbCommit] = lost
		res, err := f.move(t, false)
		if err != nil {
			t.Fatalf("move: %v", err)
		}
		if !f.log.has("verb status") || !f.log.has("mark web laptop") {
			t.Fatalf("events = %v, want status then mark", f.log.all())
		}
		if len(res.Notes) == 0 {
			t.Fatal("no note that the commit's answer was lost")
		}
	})
	t.Run("failed", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.answer[VerbCommit] = lost
		f.ep.answer[VerbStatus] = jsonAnswer(StatusResult{Present: true, MovedTo: "studio", Status: "stopped", Phase: PhaseFailed})
		_, err := f.move(t, false)
		if err == nil || f.log.has("mark ") || !strings.Contains(err.Error(), "leo agent start web") {
			t.Fatalf("move = %v (events %v), want a recoverable failure", err, f.log.all())
		}
		commit, status := f.ep.manifests[1], f.ep.manifests[2]
		if commit.Transfer == "" || status.Transfer != commit.Transfer {
			t.Fatalf("transfer ids: commit %q, status %q, want one shared id", commit.Transfer, status.Transfer)
		}
	})
	// Not live yet is not failed: the commit may still be running hooks or
	// starting the agent there.
	for name, st := range map[string]StatusResult{
		"inflight": {Present: true, Status: "stopped", Phase: PhaseHooks},
		"nophase":  {Present: true, MovedTo: "studio", Status: "stopped"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			f.ep.answer[VerbCommit] = lost
			f.ep.answer[VerbStatus] = jsonAnswer(st)
			_, err := f.move(t, false)
			if !HasCode(err, CodeOutcomeUnknown) || f.log.has("mark ") || !strings.Contains(fmt.Sprint(err), "leo --host laptop agent list") {
				t.Fatalf("move = %v (events %v), want outcome_unknown naming the check", err, f.log.all())
			}
			if strings.Contains(err.Error(), "leo agent start web") {
				t.Fatalf("move = %v: must not advise starting here while the destination may still start it", err)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.answer[VerbCommit] = lost
		f.ep.answer[VerbStatus] = lost
		_, err := f.move(t, false)
		if err == nil || f.log.has("mark ") || !strings.Contains(err.Error(), "leo --host laptop agent list") {
			t.Fatalf("move = %v, want a warning to check the destination first", err)
		}
		if strings.Contains(err.Error(), "leo agent start web") {
			t.Fatalf("move = %v: must not advise starting here while the destination may be live", err)
		}
	})
	// A refusal is settled only by the destination confirming it rolled
	// back: one that left the agent live there is no outcome.
	refused := func(Manifest) ([]byte, error) { return nil, &VerbError{Code: CodeMoveFailed, Message: "start failed"} }
	t.Run("refused-rolledback", func(t *testing.T) {
		f := newMoveFixture(t)
		rolledBack(f, refused)
		if _, err := f.move(t, false); !HasCode(err, CodeMoveFailed) || !strings.Contains(err.Error(), "leo agent start web") {
			t.Fatalf("a rolled-back refusal = %v, want a recoverable move_failed", err)
		}
	})
	t.Run("refused-live", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.answer[VerbCommit] = refused
		_, err := f.move(t, false)
		if !HasCode(err, CodeOutcomeUnknown) || f.log.has("mark ") || strings.Contains(err.Error(), "leo agent start web") {
			t.Fatalf("a refusal with the agent live there = %v (events %v), want outcome_unknown", err, f.log.all())
		}
	})
}

// rolledBack has the destination refuse the commit with refusal and report
// the failure rolled back (phase failed), as a receiver whose cleanup
// succeeded does.
func rolledBack(f *moveFixture, refusal func(Manifest) ([]byte, error)) {
	f.ep.answer[VerbCommit] = refusal
	f.ep.answer[VerbStatus] = jsonAnswer(StatusResult{Present: true, MovedTo: "studio", Status: "stopped", Phase: PhaseFailed})
}

func TestMoveMarkRetries(t *testing.T) {
	f := newMoveFixture(t)
	f.src.markFails = 2
	if _, err := f.move(t, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	f = newMoveFixture(t)
	f.src.markFails = 3
	_, err := f.move(t, false)
	if err == nil || !strings.Contains(err.Error(), "do not start it here") {
		t.Fatalf("move = %v, want the live-elsewhere warning", err)
	}
	if f.src.fence(t, "web") == nil {
		t.Fatal("a live destination left unmarked lost its fence here")
	}
}

func TestMoveRefusals(t *testing.T) {
	t.Run("samehost", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.local = true
		if _, err := f.move(t, false); !HasCode(err, CodeSameHost) || len(f.ep.manifests) != 0 {
			t.Fatalf("move to a local endpoint = %v", err)
		}
		f = newMoveFixture(t)
		f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: studio(), OK: true, StagingDir: destStage})
		if _, err := f.move(t, false); !HasCode(err, CodeSameHost) || f.log.has("stop ") {
			t.Fatalf("move to a host entry naming this host = %v", err)
		}
	})
	t.Run("moved", func(t *testing.T) {
		f := newMoveFixture(t)
		f.rec.MovedTo = "laptop"
		if err := agentstore.Save(f.home, f.rec); err != nil {
			t.Fatal(err)
		}
		if _, err := f.move(t, false); !HasCode(err, CodeAgentMoved) {
			t.Fatalf("move of a moved agent = %v", err)
		}
	})
	t.Run("harness", func(t *testing.T) {
		f := newMoveFixture(t)
		f.rec.Harness = "nosuch"
		if err := agentstore.Save(f.home, f.rec); err != nil {
			t.Fatal(err)
		}
		if _, err := f.move(t, false); err == nil || len(f.ep.manifests) != 0 {
			t.Fatalf("move of an unknown harness = %v", err)
		}
	})
	t.Run("unsafepath", func(t *testing.T) {
		f := newMoveFixture(t)
		f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: "/a b/staging"})
		if _, err := f.move(t, false); !HasCode(err, CodeUnsafePath) || f.log.has("stop ") {
			t.Fatalf("an unsafe destination path = %v", err)
		}
	})
	t.Run("locked", func(t *testing.T) {
		f := newMoveFixture(t)
		release, _ := f.src.BeginMove("web", agent.MoveModeCopy)
		defer release()
		if _, err := f.move(t, false); !HasCode(err, CodeAgentMoving) {
			t.Fatalf("move during a copy = %v", err)
		}
	})
}

func TestMovePersistentTaskWarns(t *testing.T) {
	f := newMoveFixture(t)
	f.cfg.Tasks = map[string]config.TaskConfig{"nightly": {Runtime: "persistent", Template: "web"}}
	f.cfg.Templates["web"] = f.cfg.Templates["coder"]
	res, err := f.move(t, false)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	for _, n := range res.Notes {
		if n.Code == NotePersistentTaskWarned && strings.Contains(n.Message, "nightly") {
			return
		}
	}
	t.Fatalf("notes = %+v, want a persistent-task warning", res.Notes)
}

func TestOneJobPerAgent(t *testing.T) {
	f := newMoveFixture(t)
	gate := make(chan struct{})
	f.ep.answer[VerbPreflight] = func(m Manifest) ([]byte, error) {
		<-gate
		return json.Marshal(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true, StagingDir: destStage})
	}
	jobs := NewJobs(f.mv)
	first, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"})
	if err != nil {
		t.Fatalf("first job: %v", err)
	}
	if _, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"}); !HasCode(err, CodeAgentMoving) {
		t.Fatalf("second job = %v, want agent_moving", err)
	}
	close(gate)
	st := waitJob(t, jobs, first.ID)
	if st.State != JobDone || st.Result == nil || st.Result.Host != "laptop" {
		t.Fatalf("job = %+v", st)
	}
	if !reflect.DeepEqual(st.Stages[0].Name, StageValidate) || st.Stages[len(st.Stages)-1].Name != StageMark {
		t.Fatalf("stages = %+v", st.Stages)
	}
	third, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"})
	if err != nil {
		t.Fatalf("a job after the first finished: %v", err)
	}
	waitJob(t, jobs, third.ID)
	if _, ok := jobs.Get("nope"); ok {
		t.Fatal("unknown job found")
	}
}

func TestJobRecordsFailure(t *testing.T) {
	f := newMoveFixture(t)
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), Problems: []Problem{{Code: CodeTemplateMissing, Message: "no template"}}})
	jobs := NewJobs(f.mv)
	st, err := jobs.Start(context.Background(), Request{Agent: "web", Mode: ModeMove, To: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	st = waitJob(t, jobs, st.ID)
	if st.State != JobFailed || st.Error == nil || st.Error.Code != CodeTemplateMissing || len(st.Problems) != 1 {
		t.Fatalf("job = %+v", st)
	}
}

func waitJob(t *testing.T, jobs *Jobs, id string) JobStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := jobs.Wait(ctx, id)
	if err != nil {
		t.Fatalf("waiting for job %s: %v", id, err)
	}
	return st
}

// The source reads the session store the agent runs with (an inherited
// CLAUDE_CONFIG_DIR, say), not the one its template names.
func TestMoveSessionsFromAgentEnv(t *testing.T) {
	f := newMoveFixture(t)
	inherited := t.TempDir()
	proj := filepath.Join(inherited, "projects", session.ProjectSlug(f.ws))
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "sid-1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.src.envs.env = map[string]string{"CLAUDE_CONFIG_DIR": inherited}
	if _, err := f.move(t, false); err != nil {
		t.Fatal(err)
	}
	sentFrom := func(root string) bool {
		real, _ := filepath.EvalSymlinks(root)
		return slices.ContainsFunc(f.log.all(), func(e string) bool { return strings.Contains(e, " "+real+"/projects/") })
	}
	if !sentFrom(inherited) || sentFrom(f.claude) {
		t.Fatalf("sessions not sent from the agent's store %s:\n%v", inherited, f.log.all())
	}
	if he := f.hooks.env[0]; he.AgentEnv["CLAUDE_CONFIG_DIR"] != inherited {
		t.Fatalf("pre_send env = %v", he.AgentEnv)
	}
}

func TestMoveRefusesUnresolvableEnv(t *testing.T) {
	f := newMoveFixture(t)
	f.src.envs.err = errors.New(`$ACCT_DIR is not set`)
	if _, err := f.move(t, false); err == nil || !strings.Contains(err.Error(), "ACCT_DIR") || f.log.has("stop ") {
		t.Fatalf("move = %v (events %v), want the env error before any stop", err, f.log.all())
	}
}

// The workspace rsync leaves the destination's nested paths alone, except a
// repository this host also has there, which it sends as usual. Where this
// host has files under a kept path, a note says they were not sent.
func TestMoveProtectsDestinationNestedPaths(t *testing.T) {
	f := newMoveFixture(t)
	gittest.Write(t, filepath.Join(f.ws, "services/api/x.txt"), "x\n")
	if err := os.MkdirAll(filepath.Join(f.ws, "vendor/shared/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.ep.answer[VerbPreflight] = jsonAnswer(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true,
		SessionRoot: "/Users/me/.claude-work", StagingDir: destStage, WSState: WSOurs, Nested: []NestedPath{
			{Path: ".claude/worktrees/sib", Kind: NestedWorktree},
			{Path: "services/api", Kind: NestedAgent},
			{Path: "vendor/lib", Kind: NestedRepo},
			{Path: "vendor/shared", Kind: NestedRepo},
		}})
	res, err := f.move(t, false)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if want := "run rsync -a --delete --itemize-changes --stats -e ssh -o BatchMode=yes --exclude=node_modules/ --exclude=/.worktrees/ " +
		"--exclude=/.claude/worktrees/sib/ --exclude=/services/api/ --exclude=/vendor/lib/ " + f.ws + "/ u@laptop:" + f.ws + "/"; !f.log.has(want) {
		t.Fatalf("events:\n%s\nwant %s", strings.Join(f.log.all(), "\n"), want)
	}
	var kept []string
	for _, n := range res.Notes {
		if n.Code == NoteDestNestedKept {
			kept = append(kept, n.Message)
		}
	}
	if len(kept) != 1 || !strings.Contains(kept[0], "services/api") {
		t.Fatalf("notes = %+v, want one kept-path note for services/api", res.Notes)
	}
}

// Until a commit's outcome is known the source is fenced on disk: a start
// (by any daemon, restarted or not) and a second move are refused.
func TestMoveUnknownOutcomeFencesSource(t *testing.T) {
	f := newMoveFixture(t)
	f.ep.answer[VerbCommit] = func(Manifest) ([]byte, error) { return nil, errors.New("ssh: exit status 255") }
	f.ep.answer[VerbStatus] = jsonAnswer(StatusResult{Present: true, Status: "stopped", Phase: PhaseHooks})
	if _, err := f.move(t, false); !HasCode(err, CodeOutcomeUnknown) {
		t.Fatalf("move = %v, want outcome_unknown", err)
	}
	p := f.src.fence(t, "web")
	if p == nil || p.To != "laptop" || p.Peer != "laptop" || p.RunID != f.ep.manifests[1].Transfer || !p.Since.Equal(receiveTime) {
		t.Fatalf("fence = %+v, want laptop, run %s", p, f.ep.manifests[1].Transfer)
	}
	mgr := agent.New(func() (*config.Config, error) { return f.cfg, nil }, idleSupervisor{}, "", "")
	if err := mgr.Start("web"); !errors.Is(err, agent.ErrMovePending) {
		t.Fatalf("Start of the fenced source = %v, want ErrMovePending", err)
	}
	f.ep.answer[VerbStatus] = jsonAnswer(StatusResult{Present: true, Status: "running", Live: true})
	if _, err := f.move(t, false); !HasCode(err, CodeMovePending) || strings.Count(strings.Join(f.log.all(), "\n"), "verb preflight") != 1 {
		t.Fatalf("second move = %v (events %v), want move_pending before any preflight", err, f.log.all())
	}
}

// A settled outcome lifts the fence: a refusal or a failure there leaves
// the source startable, and a live destination marks it moved.
func TestMoveSettledOutcomeLiftsFence(t *testing.T) {
	lost := func(Manifest) ([]byte, error) { return nil, errors.New("ssh: exit status 255") }
	cases := map[string]func(f *moveFixture){
		"refused": func(f *moveFixture) {
			rolledBack(f, func(Manifest) ([]byte, error) { return nil, &VerbError{Code: CodeMoveFailed, Message: "start failed"} })
		},
		"failed": func(f *moveFixture) {
			f.ep.answer[VerbCommit] = lost
			f.ep.answer[VerbStatus] = jsonAnswer(StatusResult{Present: true, MovedTo: "studio", Status: "stopped", Phase: PhaseFailed})
		},
		"live":    func(f *moveFixture) { f.ep.answer[VerbCommit] = lost },
		"success": func(*moveFixture) {},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			setup(f)
			_, _ = f.move(t, false)
			if !f.log.has("fence web laptop") {
				t.Fatalf("events = %v, want the fence set before the commit", f.log.all())
			}
			if p := f.src.fence(t, "web"); p != nil {
				t.Fatalf("fence after a settled outcome = %+v", p)
			}
		})
	}
}

// The destination's answer is checked: one safe dir per rel, or the move
// stops with the source recoverable.
func TestMoveRefusesBadSessionTargets(t *testing.T) {
	for name, answer := range map[string]SessionTargetsResponse{
		"too few":  {},
		"unsafe":   {Targets: []string{"/a b/c"}},
		"relative": {Targets: []string{"projects/x"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			f.ep.answer[VerbSessionTargets] = jsonAnswer(answer)
			if _, err := f.move(t, false); err == nil {
				t.Fatal("move succeeded")
			}
			if f.log.has("verb commit") {
				t.Fatalf("committed after a bad answer: %v", f.log.all())
			}
		})
	}
}

// An agent whose resume transcript is not in the source's session store is
// refused before anything stops or reaches the destination: the destination
// must never resume some other session of the same workspace instead.
func TestMoveRefusesMissingSourceTranscript(t *testing.T) {
	for _, mode := range []string{ModeMove, ModeCopy} {
		t.Run(mode, func(t *testing.T) {
			f := newMoveFixture(t)
			transcript := filepath.Join(f.claude, "projects", session.ProjectSlug(f.ws), "sid-1.jsonl")
			if err := os.Remove(transcript); err != nil {
				t.Fatal(err)
			}
			_, err := f.mv.Run(context.Background(), Request{Agent: "web", Mode: mode, To: "laptop"}, nil)
			if !HasCode(err, CodeSessionMissing) || !strings.Contains(err.Error(), transcript) {
				t.Fatalf("err = %v, want session_missing naming %s", err, transcript)
			}
			if f.log.has("stop ") || f.log.has("verb ") {
				t.Fatalf("something ran before the refusal: %v", f.log.all())
			}
		})
	}
}

// The job result carries each rsync stage's itemized changes, so a move can
// be audited after the fact.
func TestMoveResultListsItemizedChangesPerStage(t *testing.T) {
	f := newMoveFixture(t)
	f.runner.answer = func(c Cmd) ([]byte, error) {
		switch {
		case strings.Contains(c.String(), "--stats"):
			return []byte(">f+++++++++ notes.txt\n*deleting   old.txt\n\nNumber of files: 2\n"), nil
		case strings.Contains(c.String(), "/projects/"):
			return []byte(">f+++++++++ sid-1.jsonl\n"), nil
		}
		return nil, nil
	}
	res, err := f.move(t, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []RsyncStage{
		{Stage: StageWorkspace, Total: 2, Lines: []string{">f+++++++++ notes.txt", "*deleting   old.txt"}},
		{Stage: StageSessions, Total: 1, Lines: []string{">f+++++++++ sid-1.jsonl"}},
		{Stage: StageStaging, Total: 0, Lines: []string{}},
	}
	if !reflect.DeepEqual(res.Rsync, want) {
		t.Fatalf("Rsync = %+v\nwant %+v", res.Rsync, want)
	}
}

// A symlink the destination reports inside a store path is refused before
// the stop when the source would send something in its place (rsync turns
// it into a real dir); one the source lacks is left alone.
func TestMoveRefusesStoreSymlinkConflict(t *testing.T) {
	for name, tc := range map[string]struct {
		sourceHas bool
		refused   bool
	}{
		"source sends a dir there": {sourceHas: true, refused: true},
		"source has nothing there": {sourceHas: false, refused: false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			slug := session.ProjectSlug(f.ws)
			rel := "projects/" + slug + "/memory"
			if tc.sourceHas {
				if err := os.MkdirAll(filepath.Join(f.claude, rel), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f.ep.answer[VerbPreflight] = func(m Manifest) ([]byte, error) {
				if !slices.Contains(m.SessionRels, "projects/"+slug+"/") {
					return nil, fmt.Errorf("preflight manifest lacks the session rels: %q", m.SessionRels)
				}
				return json.Marshal(PreflightResponse{Protocol: Protocol, Host: laptopHost(), OK: true,
					SessionRoot: "/Users/me/.claude-work", StagingDir: destStage, WSState: WSAbsent, StoreSymlinks: []string{rel}})
			}
			_, err := f.move(t, false)
			if !tc.refused {
				if err != nil {
					t.Fatalf("move: %v", err)
				}
				return
			}
			if !HasCode(err, CodeStoreSymlinkConflict) || !strings.Contains(err.Error(), rel) {
				t.Fatalf("err = %v, want store_symlink_conflict naming %s", err, rel)
			}
			if f.log.has("stop ") {
				t.Fatalf("stopped before refusing: %v", f.log.all())
			}
		})
	}
}

// A symlink inside a source store path is sent as a symlink, so it must
// still resolve on the destination: only a relative link to something the
// same transfer carries is allowed. Others are refused before the stop.
func TestMoveRefusesSourceStoreSymlinks(t *testing.T) {
	for name, tc := range map[string]struct {
		target  func(f *moveFixture, proj string) string
		refused bool
	}{
		"outside the store": {target: func(*moveFixture, string) string { return t.TempDir() }, refused: true},
		"dangling":          {target: func(*moveFixture, string) string { return "gone.jsonl" }, refused: true},
		"absolute inside":   {target: func(_ *moveFixture, proj string) string { return filepath.Join(proj, "sid-1.jsonl") }, refused: true},
		"escapes the rel":   {target: func(*moveFixture, string) string { return "../other" }, refused: true},
		"relative inside":   {target: func(*moveFixture, string) string { return "sid-1.jsonl" }, refused: false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			proj := filepath.Join(f.claude, "projects", session.ProjectSlug(f.ws))
			if err := os.MkdirAll(filepath.Join(f.claude, "projects", "other"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target(f, proj), filepath.Join(proj, "link")); err != nil {
				t.Fatal(err)
			}
			_, err := f.move(t, false)
			if !tc.refused {
				if err != nil {
					t.Fatalf("move: %v", err)
				}
				return
			}
			rel := filepath.Join("projects", session.ProjectSlug(f.ws), "link")
			if !HasCode(err, CodeStoreSymlinkSource) || !strings.Contains(err.Error(), rel) {
				t.Fatalf("err = %v, want store_symlink_source naming %s", err, rel)
			}
			if f.log.has("stop ") || f.log.has("verb ") {
				t.Fatalf("something ran before the refusal: %v", f.log.all())
			}
		})
	}
}
