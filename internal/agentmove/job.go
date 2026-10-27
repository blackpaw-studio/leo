package agentmove

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// Job stages, in the order a move reaches them. A job reports each as it
// starts, so the last one reported is where a failure happened.
const (
	StageValidate   = "validate"
	StagePreflight  = "preflight"
	StageSeed       = "seed"
	StageIdle       = "idle-check"
	StageStop       = "stop"
	StagePreSend    = "pre-send"
	StageSnapshot   = "snapshot"
	StagePush       = "push"
	StageGitPrepare = "git-prepare"
	StageWorkspace  = "rsync-workspace"
	StageSessions   = "rsync-sessions"
	StageStaging    = "rsync-staging"
	StageModules    = "rsync-modules"
	StageCommit     = "commit"
	StageMark       = "mark"
)

// Codes a source job adds.
const (
	// CodeAgentMoving: another move or copy of the agent is in flight.
	CodeAgentMoving = "agent_moving"
	// CodeOutcomeUnknown: the destination commit did not answer and its
	// status could not be read.
	CodeOutcomeUnknown = "outcome_unknown"
	// NoteCommitAnswerLost: the commit's answer was lost but the agent is
	// running on the destination.
	NoteCommitAnswerLost = "commit_answer_lost"
	// NoteCleanupFailed: a temp ref could not be deleted (G5).
	NoteCleanupFailed = "cleanup_failed"
)

// markAttempts is how many times S7 tries to mark the source moved.
const markAttempts = 3

// Request asks for one move or copy of an agent.
type Request struct {
	Agent string `json:"agent"`
	Mode  string `json:"mode"`
	// To is a client.hosts key; "" or "localhost" is this host.
	To string `json:"to,omitempty"`
	// Force moves a busy agent anyway, cutting its turn off.
	Force bool `json:"force,omitempty"`
	// Name and Branch override a copy's new name and branch.
	Name   string `json:"name,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// Result is a finished transfer: the agent's identity on the destination.
type Result struct {
	Name string `json:"name"`
	// Host is the destination's hostname, To its client.hosts key.
	Host      string    `json:"host"`
	To        string    `json:"to,omitempty"`
	Workspace string    `json:"workspace"`
	Branch    string    `json:"branch,omitempty"`
	Notes     []Problem `json:"notes,omitempty"`
}

// Progress hears a job's stages and notes as they happen.
type Progress interface {
	Stage(name string)
	Note(p Problem)
}

type nopProgress struct{}

func (nopProgress) Stage(string) {}
func (nopProgress) Note(Problem) {}

// SourceManager is the part of agent.Manager a source job needs.
type SourceManager interface {
	BeginMove(name string, mode agent.MoveMode) (release func(), err error)
	Stop(name string, opts agent.StopOptions) error
	MarkMoved(name, to, peer string, at time.Time) error
}

// BusyProber reports whether an agent is mid-turn.
type BusyProber interface {
	Probe(ctx context.Context, name string) (drain.Probe, error)
}

// Mover runs moves on the source daemon. Every external effect goes through
// a field, so tests drive the whole sequence.
type Mover struct {
	Config func() (*config.Config, error)
	Agents SourceManager
	// Endpoint resolves a Request's To.
	Endpoint func(cfg *config.Config, to string) (Endpoint, error)
	Runner   Runner
	Hooks    HookRunner
	Prober   BusyProber
	Clock    Clock
	Host     func() (Host, error)
}

// NewMover returns a Mover with the real exec, hooks, clock and host; local
// serves verbs for a destination that is this host.
func NewMover(cfg func() (*config.Config, error), agents SourceManager, prober BusyProber, local Handler) *Mover {
	runner := ExecRunner{}
	return &Mover{
		Config: cfg, Agents: agents, Runner: runner, Hooks: ExecHooks{}, Prober: prober, Clock: realClock{}, Host: LocalHost,
		Endpoint: func(c *config.Config, to string) (Endpoint, error) { return ResolveEndpoint(c, to, runner, local) },
	}
}

// ResolveEndpoint is the destination named by to: this host for "" or
// "localhost", else the client.hosts entry over ssh.
func ResolveEndpoint(cfg *config.Config, to string, runner Runner, local Handler) (Endpoint, error) {
	if to == "" || to == config.LocalhostSentinel {
		return NewLocalEndpoint(local), nil
	}
	res, err := cfg.ResolveHost(to)
	if err != nil {
		return nil, problemf(CodeBadRequest, "%v", err)
	}
	if res.Localhost {
		return NewLocalEndpoint(local), nil
	}
	ep, err := NewSSHEndpoint(res, runner)
	if err != nil {
		return nil, problemf(CodeUnsafePath, "%v", err)
	}
	return ep, nil
}

// Run performs req under the agent's move lock and returns where it landed.
func (mv *Mover) Run(ctx context.Context, req Request, p Progress) (Result, error) {
	if p == nil {
		p = nopProgress{}
	}
	if req.Mode != ModeMove {
		return Result{}, problemf(CodeBadRequest, "unknown transfer mode %q", req.Mode)
	}
	release, err := mv.Agents.BeginMove(req.Agent, agent.MoveModeMove)
	if err != nil {
		return Result{}, problemf(CodeAgentMoving, "%v", err)
	}
	defer release()
	t := &transfer{mv: mv, req: req, p: p}
	if err := t.validate(ctx); err != nil {
		return Result{}, err
	}
	if err := t.preflight(ctx); err != nil {
		return Result{}, err
	}
	if err := t.checkIdle(ctx); err != nil {
		return Result{}, err
	}
	return t.send(ctx)
}

// transfer is one Run's state.
type transfer struct {
	mv      *Mover
	req     Request
	p       Progress
	cfg     *config.Config
	rec     agentstore.Record
	others  []string
	adapter harness.SessionMover
	ep      Endpoint
	host    Host
	git     GitInfo
	m       Manifest
	resp    PreflightResponse
	moveCfg config.ResolvedMove
	env     map[string]string
	prep    GitPrepareResult
	notes   []Problem
}

func (t *transfer) note(p Problem) {
	t.notes = append(t.notes, p)
	t.p.Note(p)
}

// validate is P0: the record, harness, paths, endpoint and git mode, all
// checked before anything leaves this host.
func (t *transfer) validate(ctx context.Context) error {
	t.p.Stage(StageValidate)
	var err error
	if t.cfg, err = t.mv.Config(); err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	records, err := agentstore.Load(agentstore.FilePath(t.cfg.HomePath))
	if err != nil {
		return fmt.Errorf("loading agents: %w", err)
	}
	rec, ok := records[t.req.Agent]
	switch {
	case !ok:
		return problemf(CodeBadRequest, "agent %q not found", t.req.Agent)
	case rec.Moved():
		return problemf(CodeAgentMoved, "%v", movedText(rec))
	case rec.Workspace == "":
		return problemf(CodeBadRequest, "agent %q has no workspace", rec.Name)
	}
	t.rec = rec
	for name, r := range records {
		if name != rec.Name && r.Workspace != "" {
			t.others = append(t.others, r.Workspace)
		}
	}
	slices.Sort(t.others)
	if err := t.validateAgent(); err != nil {
		return err
	}
	if t.ep, err = t.mv.Endpoint(t.cfg, t.req.To); err != nil {
		return err
	}
	if t.ep.Local() {
		return problemf(CodeSameHost, "%s is already on this host; name another host with --to", rec.Name)
	}
	if t.host, err = t.mv.Host(); err != nil {
		return err
	}
	if err := t.detectGit(ctx); err != nil {
		return err
	}
	t.moveCfg = t.cfg.ResolveMove(rec.Template)
	t.m = Manifest{Protocol: Protocol, Mode: t.req.Mode, From: t.host, Agent: rec.Portable(), SourceSessionID: agent.ResumeIDFor(rec)}
	if t.m.Git, err = t.git.Manifest(ctx, rec.Workspace, rec.Repo); err != nil {
		return err
	}
	t.env = agentEnv(t.cfg, t.cfg.Templates[rec.Template], t.m)
	if task, bound := agent.PersistentTaskFor(t.cfg, rec.Name); bound {
		t.note(Problem{Code: NotePersistentTaskWarned, Message: fmt.Sprintf(
			"%s backs persistent task %q: its deliveries fail on this host until the agent moves back", rec.Name, task)})
	}
	return nil
}

func movedText(rec agentstore.Record) error {
	e := &agent.MovedError{Name: rec.Name, To: rec.MovedTo}
	if rec.MovedAt != nil {
		e.At = *rec.MovedAt
	}
	return e
}

// validateAgent checks the harness can move sessions and the name and
// workspace are safe on the wire.
func (t *transfer) validateAgent() error {
	h, err := harness.Get(orClaude(t.rec.Harness))
	if err != nil {
		return problemf(CodeHarnessUnsupported, "%v", err)
	}
	adapter, ok := h.(harness.SessionMover)
	if !ok {
		return problemf(CodeHarnessUnsupported, "%s agents cannot be moved", h.Name())
	}
	t.adapter = adapter
	if err := ValidateRef(t.rec.Name); err != nil {
		return err
	}
	return ValidatePath(t.rec.Workspace)
}

// detectGit classifies the workspace and refuses what a move cannot carry.
func (t *transfer) detectGit(ctx context.Context) error {
	ws := t.rec.Workspace
	g, err := DetectGit(ctx, ws, t.rec.CanonicalPath)
	if err != nil {
		return err
	}
	if g.Mode != GitModePlain {
		if err := CheckGitMode(ctx, ws, g); err != nil {
			return err
		}
		if err := ValidatePath(g.Canonical); err != nil {
			return err
		}
		if b := g.HeadRef; b != "" {
			if err := ValidateRef(strings.TrimPrefix(b, "refs/heads/")); err != nil {
				return err
			}
		}
	}
	t.git = g
	for _, n := range g.Notes() {
		t.note(n)
	}
	return nil
}

func (t *transfer) gitMode() bool { return t.git.Mode != GitModePlain }

// preflight is P1 (and P3): the destination's verdict, re-asked after a
// main-mode promotion or a seed clone changes the facts.
func (t *transfer) preflight(ctx context.Context) error {
	t.p.Stage(StagePreflight)
	if err := t.ask(ctx); err != nil {
		return err
	}
	if t.git.IsRepo && !t.gitMode() && t.resp.Git != nil && t.resp.Git.LinkedWorktrees > 0 {
		if err := t.promoteMain(ctx); err != nil {
			return err
		}
	}
	if g := t.resp.Git; t.gitMode() && g != nil && !g.CanonicalPresent && g.Cloneable {
		t.p.Stage(StageSeed)
		if _, err := t.ep.Run(ctx, VerbGitSeed, t.body()); err != nil {
			return err
		}
		if err := t.ask(ctx); err != nil {
			return err
		}
	}
	if !t.resp.OK {
		return Problems(t.resp.Problems)
	}
	if t.resp.Host.Hostname == t.host.Hostname {
		return problemf(CodeSameHost, "%s reaches this host (%s)", t.ep.Name(), t.host.Hostname)
	}
	for _, p := range []string{t.resp.SessionRoot, t.resp.StagingDir} {
		if p != "" {
			if err := ValidatePath(p); err != nil {
				return err
			}
		}
	}
	if last, ok := t.rec.MoveLog[t.resp.Host.Hostname]; ok {
		t.m.LastMove = &last
	}
	return t.checkDiverged(ctx)
}

// ask sends a preflight and keeps its answer.
func (t *transfer) ask(ctx context.Context) error {
	out, err := t.ep.Run(ctx, VerbPreflight, t.body())
	if err != nil {
		return err
	}
	var resp PreflightResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("decoding the preflight answer from %s: %w", t.ep.Name(), err)
	}
	t.resp = resp
	return nil
}

// promoteMain turns a plain move into a main-mode one: the destination's
// repository at the workspace has linked worktrees, so its .git must stay.
func (t *transfer) promoteMain(ctx context.Context) error {
	ws := t.rec.Workspace
	g, err := PromoteMain(ctx, ws, t.git)
	if err != nil {
		return err
	}
	if err := CheckGitMode(ctx, ws, g); err != nil {
		return err
	}
	t.git = g
	if t.m.Git, err = g.Manifest(ctx, ws, t.rec.Repo); err != nil {
		return err
	}
	return t.ask(ctx)
}

// checkDiverged refuses early when the destination branch has commits the
// source lacks; G3 re-checks with the pushed objects.
func (t *transfer) checkDiverged(ctx context.Context) error {
	g := t.resp.Git
	if !t.gitMode() || g == nil || g.BranchSHA == "" || g.BranchSHA == t.git.HeadSHA {
		return nil
	}
	ws := t.rec.Workspace
	if git.HasCommit(ctx, ws, g.BranchSHA) {
		ok, err := git.IsAncestor(ctx, ws, g.BranchSHA, "HEAD")
		if err != nil || ok {
			return err
		}
	}
	return problemf(CodeBranchDiverged, "branch %s on %s has commits this workspace lacks (%s); bring them here first",
		t.m.TargetBranch(), t.resp.Host.Hostname, g.BranchSHA)
}

// checkIdle is P2: a move cuts no turn off unless forced.
func (t *transfer) checkIdle(ctx context.Context) error {
	if t.req.Force {
		return nil
	}
	t.p.Stage(StageIdle)
	pr, err := t.mv.Prober.Probe(ctx, t.rec.Name)
	switch {
	case err != nil:
		return problemf(CodeAgentBusy, "could not tell whether %s is mid-turn (%v); pass --force to move it anyway", t.rec.Name, err)
	case pr.Busy:
		return problemf(CodeAgentBusy, "%s is mid-turn; wait for the turn to finish, or pass --force to cut it off", t.rec.Name)
	}
	return nil
}

func (t *transfer) body() []byte {
	b, _ := json.Marshal(t.m) //nolint:errchkjson // plain structs and maps
	return b
}

// send runs S1 to S7: from the stop on, a failure leaves the source
// stopped and unmarked, which `leo agent start` recovers.
func (t *transfer) send(ctx context.Context) (Result, error) {
	t.p.Stage(StageStop)
	if err := t.mv.Agents.Stop(t.rec.Name, agent.StopOptions{}); err != nil {
		return Result{}, fmt.Errorf("stopping %s: %w", t.rec.Name, err)
	}
	if err := t.transfer(ctx); err != nil {
		return Result{}, t.recoverable(err)
	}
	if err := t.commit(ctx); err != nil {
		return Result{}, err
	}
	if err := t.mark(); err != nil {
		return Result{}, err
	}
	return Result{
		Name: t.m.TargetName(), Host: t.resp.Host.Hostname, To: t.ep.Name(),
		Workspace: t.m.TargetWorkspace(), Branch: t.m.TargetBranch(), Notes: t.notes,
	}, nil
}

// recoverable says how to get a stopped, unmarked source back.
func (t *transfer) recoverable(err error) error {
	return fmt.Errorf("%w; %s is stopped here and not marked moved: `leo agent start %s` recovers it", err, t.rec.Name, t.rec.Name)
}

// transfer is S2 to S5m: the final session id, pre_send, the harness
// staging, the git snapshot and push, then every rsync.
func (t *transfer) transfer(ctx context.Context) error {
	ids, err := t.finalSessions()
	if err != nil {
		return err
	}
	stage, err := t.prepareStaging(ctx, ids)
	if err != nil {
		return err
	}
	if t.gitMode() {
		cleanup, err := t.pushGit(ctx)
		defer cleanup()
		if err != nil {
			return err
		}
	}
	return t.rsyncAll(ctx, stage, ids)
}

// finalSessions is S2: the session id to resume, read after the stop.
func (t *transfer) finalSessions() ([]string, error) {
	records, err := agentstore.Load(agentstore.FilePath(t.cfg.HomePath))
	if err != nil {
		return nil, fmt.Errorf("reloading %s: %w", t.rec.Name, err)
	}
	if rec, ok := records[t.rec.Name]; ok {
		t.rec = rec
	}
	t.m.SourceSessionID = agent.ResumeIDFor(t.rec)
	ids := []string{t.m.SourceSessionID}
	for _, id := range t.rec.SessionsByTemplate {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids[1:])
	return ids, nil
}

// prepareStaging is S3 and S4: a fresh staging dir, pre_send hooks, then
// the harness's own staging.
func (t *transfer) prepareStaging(ctx context.Context, ids []string) (string, error) {
	t.p.Stage(StagePreSend)
	stage := StagingDir(t.cfg.HomePath, t.rec.Name)
	if err := ValidatePath(stage); err != nil {
		return "", err
	}
	if err := os.RemoveAll(stage); err != nil {
		return "", fmt.Errorf("clearing %s: %w", stage, err)
	}
	for _, sub := range []string{"hooks", "harness"} {
		if err := os.MkdirAll(filepath.Join(stage, sub), 0o700); err != nil {
			return "", err
		}
	}
	he := HookEnv{
		Agent: t.rec.Name, Workspace: t.rec.Workspace, SessionID: t.m.SourceSessionID,
		From: t.host.Hostname, To: t.resp.Host.Hostname, Dir: filepath.Join(stage, "hooks"), Mode: t.req.Mode,
		SourceAgent: t.rec.Name, SourceWorkspace: t.rec.Workspace, SourceSessionID: t.m.SourceSessionID, AgentEnv: t.env,
	}
	if t.m.LastMove != nil {
		he.Last = *t.m.LastMove
	}
	if err := t.mv.Hooks.RunHooks(ctx, "pre_send", t.moveCfg.PreSend, t.moveCfg.HookTimeout, he); err != nil {
		return "", err
	}
	mc := harness.MoveContext{Env: t.env, Workspace: t.rec.Workspace, SessionIDs: ids, Local: t.ep.Local()}
	if err := t.adapter.PrepareSend(ctx, mc, stage); err != nil {
		return "", err
	}
	return stage, nil
}

// pushGit is G1 to G3: snapshot the index, push the head and snapshot into
// the destination canonical, and prepare the destination worktree. cleanup
// deletes the local temp refs (G5) and always runs.
func (t *transfer) pushGit(ctx context.Context) (cleanup func(), err error) {
	ws, name := t.rec.Workspace, t.m.TargetName()
	headRef, indexRef := MoveRef(name, "head"), MoveRef(name, "index")
	refs := []string{indexRef}
	cleanup = func() {
		if err := git.DeleteRefs(context.WithoutCancel(ctx), ws, refs...); err != nil {
			t.note(Problem{Code: NoteCleanupFailed, Message: fmt.Sprintf("deleting %v in %s: %v", refs, ws, err)})
		}
	}
	t.p.Stage(StageSnapshot)
	if err := t.snapshot(ctx, indexRef); err != nil {
		return cleanup, err
	}
	t.p.Stage(StagePush)
	refspecs := []string{"+" + t.m.Git.HeadSHA + ":" + headRef, "+" + indexRef + ":" + indexRef}
	c, remote, err := t.ep.Push(ws, t.m.Git.Canonical, t.resp.Git.GitPath, refspecs)
	switch {
	case err != nil:
		return cleanup, err
	case remote:
		if _, err := t.mv.Runner.Run(ctx, c); err != nil {
			return cleanup, fmt.Errorf("pushing to %s: %w", t.ep.Name(), err)
		}
	default:
		// One repository: the head ref G3 reads is written here.
		refs = append(refs, headRef)
		if err := git.UpdateRef(ctx, ws, headRef, t.m.Git.HeadSHA); err != nil {
			return cleanup, err
		}
	}
	t.p.Stage(StageGitPrepare)
	out, err := t.ep.Run(ctx, VerbGitPrepare, t.body())
	if err != nil {
		return cleanup, err
	}
	if err := json.Unmarshal(out, &t.prep); err != nil {
		return cleanup, fmt.Errorf("decoding the git-prepare answer: %w", err)
	}
	for _, p := range []string{t.prep.ModulesDir, t.prep.GitDir} {
		if err := ValidatePath(p); err != nil {
			return cleanup, err
		}
	}
	return cleanup, nil
}

// snapshot is G1: HEAD as it is after the stop (the agent may have
// committed since P0) and the index as a commit on ref.
func (t *transfer) snapshot(ctx context.Context, ref string) error {
	ws := t.rec.Workspace
	if marker, err := git.OpInProgress(ctx, ws); err != nil || marker != "" {
		if err != nil {
			return err
		}
		return problemf(CodeGitOpInProgress, "a git %s started in %s; finish or abort it first", opNames[marker], ws)
	}
	head, err := git.HeadState(ctx, ws)
	if err != nil {
		return problemf(CodeGitUnsupported, "reading HEAD: %v", err)
	}
	if b := head.Branch(); b != "" {
		if err := ValidateRef(b); err != nil {
			return err
		}
	}
	t.m.Git.HeadRef, t.m.Git.HeadSHA = head.Ref, head.SHA
	commit, err := git.SnapshotIndex(ctx, ws, ref)
	if err != nil {
		return fmt.Errorf("snapshotting the index: %w", err)
	}
	t.m.Git.IndexCommit = commit
	return nil
}

// rsyncAll is S5 and S5m: workspace, session files, staging, then the
// private submodule git dirs.
func (t *transfer) rsyncAll(ctx context.Context, stage string, ids []string) error {
	ws, shell := t.rec.Workspace, t.ep.RsyncShell()
	spec := t.git.WorkspaceSpec(ws, t.moveCfg.Exclude, t.others)
	t.p.Stage(StageWorkspace)
	if err := t.rsync(ctx, WorkspaceRsyncArgv(shell, ws, t.ep.Target(ws), spec.Excludes())); err != nil {
		return err
	}
	if err := t.rsyncSessions(ctx, ids); err != nil {
		return err
	}
	t.p.Stage(StageStaging)
	if err := t.rsync(ctx, StagingRsyncArgv(shell, stage, t.ep.Target(t.resp.StagingDir))); err != nil {
		return err
	}
	src := t.git.ModulesDir
	if !t.gitMode() || src == "" || !isDir(src) {
		return nil
	}
	t.p.Stage(StageModules)
	dst := ModulesTarget(false, t.prep.ModulesDir, t.prep.GitDir)
	if err := ValidatePath(src); err != nil {
		return err
	}
	return t.rsync(ctx, ModulesRsyncArgv(shell, src, t.ep.Target(dst)))
}

func (t *transfer) rsyncSessions(ctx context.Context, ids []string) error {
	root := t.adapter.SessionRoot(t.env)
	if root == "" || t.resp.SessionRoot == "" {
		return nil
	}
	rels, err := t.adapter.SessionFiles(root, t.rec.Workspace, ids)
	if err != nil || len(rels) == 0 {
		return err
	}
	for _, rel := range rels {
		if strings.HasPrefix(rel, "-") || filepath.IsAbs(rel) {
			return problemf(CodeUnsafePath, "session path %q must be relative to %s", rel, root)
		}
		if err := ValidatePath(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	t.p.Stage(StageSessions)
	return t.run(ctx, SessionRsyncCmd(t.ep.RsyncShell(), root, rels, t.ep.Target(t.resp.SessionRoot)))
}

func (t *transfer) rsync(ctx context.Context, argv []string) error {
	return t.run(ctx, Cmd{Name: "rsync", Args: argv})
}

func (t *transfer) run(ctx context.Context, c Cmd) error {
	if _, err := t.mv.Runner.Run(ctx, c); err != nil {
		return fmt.Errorf("%s to %s: %w", c.Name, t.ep.Name(), err)
	}
	return nil
}

// commit is S6. A refusal leaves the source recoverable; an answer lost on
// the way back is settled by asking the destination what it holds.
func (t *transfer) commit(ctx context.Context) error {
	t.p.Stage(StageCommit)
	out, err := t.ep.Run(ctx, VerbCommit, t.body())
	if err == nil {
		var res CommitResult
		if err := json.Unmarshal(out, &res); err == nil {
			for _, n := range res.Notes {
				t.note(n)
			}
		}
		return nil
	}
	if t.ep.Local() || Definite(err) {
		return t.recoverable(err)
	}
	st, serr := t.status(ctx)
	switch {
	case serr != nil:
		return problemf(CodeOutcomeUnknown,
			"the commit on %s did not answer (%v) and its status cannot be read (%v); %s may be live there: check `leo --host %s agent list` before starting it here",
			t.ep.Name(), err, serr, t.rec.Name, t.ep.Name())
	case !st.Live:
		return t.recoverable(fmt.Errorf("the commit on %s failed (%w); %s is not running there", t.ep.Name(), err, t.rec.Name))
	}
	t.note(Problem{Code: NoteCommitAnswerLost, Message: fmt.Sprintf("the commit's answer was lost (%v), but %s is running on %s", err, t.rec.Name, t.ep.Name())})
	return nil
}

func (t *transfer) status(ctx context.Context) (StatusResult, error) {
	out, err := t.ep.Run(ctx, VerbStatus, t.body())
	if err != nil {
		return StatusResult{}, err
	}
	var st StatusResult
	if err := json.Unmarshal(out, &st); err != nil {
		return StatusResult{}, err
	}
	return st, nil
}

// mark is S7: the destination is live, so the source becomes a shadow.
func (t *transfer) mark() error {
	t.p.Stage(StageMark)
	var err error
	for range markAttempts {
		if err = t.mv.Agents.MarkMoved(t.rec.Name, t.ep.Name(), t.resp.Host.Hostname, t.mv.Clock.Now()); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s is live on %s, but marking it moved here failed: %w; do not start it here", t.rec.Name, t.ep.Name(), err)
}
