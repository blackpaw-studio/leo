package agentmove

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/blackpaw-studio/leo/internal/redact"
	"github.com/blackpaw-studio/leo/internal/session"
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
	// Rsync lists what each rsync stage changed, in the order they ran.
	Rsync []RsyncStage `json:"rsync,omitempty"`
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
	// RuntimeEnv is the env the stored agent runs with on this host.
	RuntimeEnv(rec agentstore.Record) (map[string]string, *redact.Secrets, error)
	BeginMove(name string, mode agent.MoveMode) (release func(), err error)
	Stop(name string, opts agent.StopOptions) error
	// MarkMoved marks the agent moved and lifts its fence.
	MarkMoved(name, to, peer string, at time.Time) error
	// FenceMove and ClearMoveFence set and lift the durable fence a move
	// holds from its commit until the outcome is known.
	FenceMove(name string, p agentstore.MovePending) error
	ClearMoveFence(name, runID string) error
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
	lock := map[string]agent.MoveMode{ModeMove: agent.MoveModeMove, ModeCopy: agent.MoveModeCopy}[req.Mode]
	if lock == "" {
		return Result{}, problemf(CodeBadRequest, "unknown transfer mode %q", req.Mode)
	}
	release, err := mv.Agents.BeginMove(req.Agent, lock)
	if err != nil {
		return Result{}, problemf(CodeAgentMoving, "%v", err)
	}
	defer release()
	t := &transfer{mv: mv, req: req, p: p}
	defer t.deleteTempRefs(ctx)
	res, err := t.execute(ctx)
	res.Notes = redactProblems(t.scrub, res.Notes)
	return res, redactError(t.scrub, err)
}

// execute is Run past the move lock: every phase, in order.
func (t *transfer) execute(ctx context.Context) (Result, error) {
	if err := t.validate(ctx); err != nil {
		return Result{}, err
	}
	if err := t.preflight(ctx); err != nil {
		return Result{}, err
	}
	if err := t.checkIdle(ctx); err != nil {
		return Result{}, err
	}
	if t.isCopy() {
		return t.sendCopy(ctx)
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
	// scrub replaces the agent's resolved secret values in what the run
	// keeps or returns: hook and harness output reaches its errors.
	scrub *redact.Secrets
	prep  GitPrepareResult
	notes []Problem
	// rsyncs are the itemized changes of each rsync stage run so far.
	rsyncs []RsyncStage
	// A copy's candidate names, its source branch and pre-issued fork id.
	names     []string
	curBranch string
	forkID    string
	// tempRefs are this host's move refs, deleted when the run ends (G5).
	tempRefs []string
}

func (t *transfer) isCopy() bool { return t.req.Mode == ModeCopy }

func (t *transfer) note(p Problem) {
	p.Message = t.scrub.String(p.Message)
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
	case rec.MovePending != nil:
		return problemf(CodeMovePending, "%v", &agent.MovePendingError{Name: rec.Name, Pending: *rec.MovePending})
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
	if t.ep.Local() && !t.isCopy() {
		return problemf(CodeSameHost, "%s is already on this host; name another host with --to", rec.Name)
	}
	if t.host, err = t.mv.Host(); err != nil {
		return err
	}
	if err := t.detectGit(ctx); err != nil {
		return err
	}
	t.moveCfg = t.cfg.ResolveMove(rec.Template)
	t.m = Manifest{Protocol: Protocol, Mode: t.req.Mode, From: t.host, Agent: rec.Portable(), SourceSessionID: agent.ResumeIDFor(rec), Transfer: session.NewID()}
	if t.m.Git, err = t.git.Manifest(ctx, rec.Workspace, rec.Repo); err != nil {
		return err
	}
	if t.env, t.scrub, err = t.mv.Agents.RuntimeEnv(rec); err != nil {
		return problemf(CodeEnvironmentMissing, "%s's env cannot be resolved here: %v", rec.Name, err)
	}
	if err := t.checkTranscript(); err != nil {
		return err
	}
	if t.m.SessionRels, err = t.sessionRels(sessionIDs(t.rec)); err != nil {
		return err
	}
	if err := checkSourceSymlinks(t.adapter.SessionRoot(t.env), t.m.SessionRels); err != nil {
		return err
	}
	t.notePersistentTask()
	if t.isCopy() {
		return t.validateCopy(ctx, records)
	}
	return nil
}

// checkTranscript refuses an agent whose resume session has no transcript in
// the source's store. Nothing has stopped yet, and without it the
// destination could resume another session of the same workspace.
func (t *transfer) checkTranscript() error {
	loc, ok := t.adapter.(harness.TranscriptLocator)
	root := t.adapter.SessionRoot(t.env)
	id := t.m.SourceSessionID
	if !ok || root == "" || id == "" {
		return nil
	}
	if searched, found := loc.Transcript(root, t.rec.Workspace, id); !found {
		return problemf(CodeSessionMissing, "%s's session %s has no transcript (looked for %s); there is nothing to resume, so it was not moved", t.rec.Name, id, searched)
	}
	return nil
}

func (t *transfer) notePersistentTask() {
	task, bound := agent.PersistentTaskFor(t.cfg, t.rec.Name)
	switch {
	case !bound:
	case t.isCopy():
		t.note(Problem{Code: NotePersistentTaskWarned, Message: fmt.Sprintf(
			"the copy is not wired to persistent task %q, which keeps delivering to %s", task, t.rec.Name)})
	default:
		t.note(Problem{Code: NotePersistentTaskWarned, Message: fmt.Sprintf(
			"%s backs persistent task %q: its deliveries fail on this host until the agent moves back", t.rec.Name, task)})
	}
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
	if err := t.askName(ctx); err != nil {
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
	if t.resp.Host.Hostname == t.host.Hostname && !t.isCopy() {
		return problemf(CodeSameHost, "%s reaches this host (%s)", t.ep.Name(), t.host.Hostname)
	}
	for _, p := range []string{t.resp.SessionRoot, t.resp.StagingDir} {
		if p != "" {
			if err := ValidatePath(p); err != nil {
				return err
			}
		}
	}
	if err := t.checkStoreSymlinks(); err != nil {
		return err
	}
	if last, ok := t.rec.MoveLog[t.resp.Host.Hostname]; ok && !t.isCopy() {
		t.m.LastMove = &last
	}
	if t.isCopy() {
		return t.checkCopyBranch(ctx)
	}
	return t.checkDiverged(ctx)
}

// checkStoreSymlinks refuses the destination store symlinks the session
// rsync would replace: the source has an entry at that path, which rsync
// sends in place of the link (a symlinked memory/ becomes a real dir).
func (t *transfer) checkStoreSymlinks() error {
	root := t.adapter.SessionRoot(t.env)
	if root == "" || (t.ep.Local() && git.SamePath(root, t.resp.SessionRoot)) {
		return nil
	}
	var hit []string
	for _, rel := range t.resp.StoreSymlinks {
		if err := checkSessionRel(rel); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); err == nil {
			hit = append(hit, rel)
		}
	}
	if len(hit) == 0 {
		return nil
	}
	return problemf(CodeStoreSymlinkConflict, "%s's session store %s on %s has symlinks where this transfer would write: %s; rsync would replace them, so nothing was stopped or sent", t.rec.Name, t.resp.SessionRoot, t.resp.Host.Hostname, strings.Join(hit, ", "))
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

// checkIdle is P2: a move cuts no turn off unless forced. A copy never
// stops the source, so only a harness that cannot snapshot the last
// completed turn refuses a busy one, forced or not.
func (t *transfer) checkIdle(ctx context.Context) error {
	if t.isCopy() {
		return t.checkCopyIdle(ctx)
	}
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
	return t.result(), nil
}

func (t *transfer) result() Result {
	return Result{
		Name: t.m.TargetName(), Host: t.resp.Host.Hostname, To: t.ep.Name(),
		Workspace: t.m.TargetWorkspace(), Branch: t.m.TargetBranch(), Notes: t.notes, Rsync: t.rsyncs,
	}
}

// recoverable says how to get a stopped, unmarked source back. A copy's
// source is never stopped: a re-run reuses its partial workspace.
func (t *transfer) recoverable(err error) error {
	if t.isCopy() {
		return err
	}
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
		if err := t.pushGit(ctx); err != nil {
			return err
		}
	}
	return t.rsyncAll(ctx, stage, ids)
}

// deleteTempRefs is G5, run when the job ends either way: after the
// commit, since a destination sharing this repository reads them in G4.
func (t *transfer) deleteTempRefs(ctx context.Context) {
	if len(t.tempRefs) == 0 {
		return
	}
	if err := git.DeleteRefs(context.WithoutCancel(ctx), t.rec.Workspace, t.tempRefs...); err != nil {
		t.note(Problem{Code: NoteCleanupFailed, Message: fmt.Sprintf("deleting %v in %s: %v", t.tempRefs, t.rec.Workspace, err)})
	}
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
	return sessionIDs(t.rec), nil
}

// sessionIDs are rec's resume session first, then its other sessions.
func sessionIDs(rec agentstore.Record) []string {
	ids := []string{agent.ResumeIDFor(rec)}
	for _, id := range rec.SessionsByTemplate {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids[1:])
	return ids
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
	mc := harness.MoveContext{Env: t.env, Workspace: t.rec.Workspace, SessionIDs: ids, Copy: t.isCopy(), Local: t.ep.Local()}
	if err := t.adapter.PrepareSend(ctx, mc, stage); err != nil {
		return "", err
	}
	return stage, nil
}

// pushGit is G1 to G3: snapshot the index, push the head and snapshot into
// the destination canonical, and prepare the destination worktree.
func (t *transfer) pushGit(ctx context.Context) error {
	ws, name := t.rec.Workspace, t.m.TargetName()
	headRef, indexRef := MoveRef(name, "head"), MoveRef(name, "index")
	t.tempRefs = []string{indexRef}
	t.p.Stage(StageSnapshot)
	if err := t.snapshot(ctx, indexRef); err != nil {
		return err
	}
	t.p.Stage(StagePush)
	refspecs := []string{"+" + t.m.Git.HeadSHA + ":" + headRef, "+" + indexRef + ":" + indexRef}
	c, remote, err := t.ep.Push(ws, t.m.Git.Canonical, t.resp.Git.GitPath, refspecs)
	switch {
	case err != nil:
		return err
	case remote:
		if _, err := t.mv.Runner.Run(ctx, c); err != nil {
			return fmt.Errorf("pushing to %s: %w", t.ep.Name(), err)
		}
	default:
		// One repository: the head ref G3 reads is written here.
		t.tempRefs = append(t.tempRefs, headRef)
		if err := git.UpdateRef(ctx, ws, headRef, t.m.Git.HeadSHA); err != nil {
			return err
		}
	}
	t.p.Stage(StageGitPrepare)
	out, err := t.ep.Run(ctx, VerbGitPrepare, t.body())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &t.prep); err != nil {
		return fmt.Errorf("decoding the git-prepare answer: %w", err)
	}
	for _, p := range []string{t.prep.ModulesDir, t.prep.GitDir} {
		if err := ValidatePath(p); err != nil {
			return err
		}
	}
	return nil
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

// protectDestNested adds the destination's nested paths to spec, with a
// note for each one holding back files of this host's; a path the source
// already excludes is left as it is.
func (t *transfer) protectDestNested(spec *WorkspaceSpec) {
	already := spec.Excludes()
	protect, kept := protectedNested(spec.Workspace, t.resp.Nested, t.git.Submodules)
	for _, p := range protect {
		if !slices.Contains(already, "/"+p+"/") {
			spec.DestNested = append(spec.DestNested, p)
		}
	}
	for _, n := range kept {
		if slices.Contains(spec.DestNested, n.Path) {
			t.note(keptNote(n, t.resp.Host.Hostname))
		}
	}
}

// rsyncAll is S5 and S5m: workspace, session files, staging, then the
// private submodule git dirs.
func (t *transfer) rsyncAll(ctx context.Context, stage string, ids []string) error {
	ws, shell := t.rec.Workspace, t.ep.RsyncShell()
	spec := t.git.WorkspaceSpec(ws, t.moveCfg.Exclude, t.others)
	t.protectDestNested(&spec)
	target := t.m.TargetWorkspace()
	if t.isCopy() {
		target += PartialSuffix
	}
	t.beginRsync(StageWorkspace)
	if err := t.rsync(ctx, WorkspaceRsyncArgv(shell, ws, t.ep.Target(target), spec.Excludes())); err != nil {
		return err
	}
	if err := t.rsyncSessions(ctx, ids); err != nil {
		return err
	}
	t.beginRsync(StageStaging)
	if err := t.rsync(ctx, StagingRsyncArgv(shell, stage, t.ep.Target(t.resp.StagingDir))); err != nil {
		return err
	}
	src := t.git.ModulesDir
	if !t.gitMode() || src == "" || !isDir(src) {
		return nil
	}
	t.beginRsync(StageModules)
	dst := ModulesTarget(t.isCopy(), t.prep.ModulesDir, t.prep.GitDir)
	if err := ValidatePath(src); err != nil {
		return err
	}
	return t.rsync(ctx, ModulesRsyncArgv(shell, src, t.ep.Target(dst)))
}

func (t *transfer) rsyncSessions(ctx context.Context, ids []string) error {
	root := t.adapter.SessionRoot(t.env)
	switch {
	case root == "" || t.resp.SessionRoot == "":
		return nil
	case t.ep.Local() && git.SamePath(root, t.resp.SessionRoot):
		return nil // the same files
	}
	rels, err := t.sessionRels(ids)
	if err != nil || len(rels) == 0 {
		return err
	}
	t.beginRsync(StageSessions)
	return t.sendSessions(ctx, root, rels)
}

// sessionRels are the session paths, relative to the source's store, that
// the session rsync sends for ids: none when the harness keeps no store or
// a copy's snapshot travels in staging instead.
func (t *transfer) sessionRels(ids []string) ([]string, error) {
	root := t.adapter.SessionRoot(t.env)
	if root == "" || (t.isCopy() && snapshotCopier(t.adapter)) {
		return nil, nil
	}
	rels, err := t.adapter.SessionFiles(root, t.rec.Workspace, ids)
	if err != nil {
		return nil, err
	}
	for _, rel := range rels {
		if err := checkSessionRel(rel); err != nil {
			return nil, err
		}
		if err := ValidatePath(filepath.Join(root, rel)); err != nil {
			return nil, err
		}
	}
	return rels, nil
}

// sendSessions asks the destination where each session path really lives
// (through any symlinked store dir), then rsyncs each straight into it from
// its own resolved source path.
func (t *transfer) sendSessions(ctx context.Context, root string, rels []string) error {
	m := t.m
	m.SessionRels = rels
	body, _ := json.Marshal(m) //nolint:errchkjson // plain structs and maps
	out, err := t.ep.Run(ctx, VerbSessionTargets, body)
	if err != nil {
		return err
	}
	var resp SessionTargetsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("decoding the session-targets answer from %s: %w", t.ep.Name(), err)
	}
	if len(resp.Targets) != len(rels) {
		return problemf(CodeBadRequest, "%s resolved %d session targets for %d paths", t.ep.Name(), len(resp.Targets), len(rels))
	}
	for i, rel := range rels {
		if err := ValidatePath(resp.Targets[i]); err != nil {
			return err
		}
		src, err := resolveSessionSource(root, rel)
		if err != nil {
			return err
		}
		if err := t.run(ctx, SessionRsyncCmd(t.ep.RsyncShell(), src, t.ep.Target(resp.Targets[i]))); err != nil {
			return err
		}
	}
	return nil
}

// resolveSessionSource is the real path of root/rel, keeping a trailing "/"
// on a directory.
func resolveSessionSource(root, rel string) (string, error) {
	real, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", filepath.Join(root, rel), err)
	}
	if err := ValidatePath(real); err != nil {
		return "", err
	}
	if strings.HasSuffix(rel, "/") {
		real += "/"
	}
	return real, nil
}

// beginRsync reports an rsync stage and opens its itemized record.
func (t *transfer) beginRsync(stage string) {
	t.p.Stage(stage)
	t.rsyncs = append(t.rsyncs, newRsyncStage(stage, nil))
}

func (t *transfer) rsync(ctx context.Context, argv []string) error {
	return t.run(ctx, Cmd{Name: "rsync", Args: argv})
}

func (t *transfer) run(ctx context.Context, c Cmd) error {
	out, err := t.mv.Runner.Run(ctx, c)
	if err != nil {
		return fmt.Errorf("%s to %s: %w", c.Name, t.ep.Name(), err)
	}
	if c.Name == "rsync" && len(t.rsyncs) > 0 {
		t.rsyncs[len(t.rsyncs)-1].add(ItemizedLines(out))
	}
	return nil
}

// commit is S6. Its answer settles a success. Any failure, refused or with
// the answer lost, is settled by asking the destination what it holds:
// only a failure it confirms it rolled back (phase failed, the agent not
// live) offers the source back, and only a lost answer with the agent live
// there finishes the move. Anything else keeps the source fenced. A copy
// leaves its source running, so its refusal is final.
func (t *transfer) commit(ctx context.Context) error {
	t.p.Stage(StageCommit)
	if err := t.fence(); err != nil {
		return err
	}
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
	refused := t.ep.Local() || Definite(err)
	if refused && t.isCopy() {
		return t.recoverable(err)
	}
	st, serr := t.status(ctx)
	switch {
	case serr != nil:
		return problemf(CodeOutcomeUnknown,
			"the commit on %s ended without an outcome (%v) and its status cannot be read (%v); %s may be live there, so it stays fenced here: check `leo --host %s agent list`, then settle it with `leo agent move %s --reconcile`",
			t.ep.Name(), err, serr, t.rec.Name, t.ep.Name(), t.rec.Name)
	case st.Phase == PhaseFailed && !st.Live:
		return t.unfence(t.recoverable(fmt.Errorf("the commit on %s failed (%w); %s is not running there", t.ep.Name(), err, t.rec.Name)))
	case st.Live && !refused:
		t.note(Problem{Code: NoteCommitAnswerLost, Message: fmt.Sprintf("the commit's answer was lost (%v), but %s is running on %s", err, t.rec.Name, t.ep.Name())})
		return nil
	}
	phase := st.Phase
	if phase == "" {
		phase = "unknown"
	}
	return problemf(CodeOutcomeUnknown,
		"the commit on %s ended without an outcome (%v) and has not settled there (phase %s, live %v); %s may still be live there, so it stays fenced here: check `leo --host %s agent list`, then settle it with `leo agent move %s --reconcile`",
		t.ep.Name(), err, phase, st.Live, t.rec.Name, t.ep.Name(), t.rec.Name)
}

// fence persists the source's move fence before the commit starts: until
// the outcome is known nothing here starts the agent, across daemon
// restarts. A copy leaves its source running and needs none.
func (t *transfer) fence() error {
	if t.isCopy() {
		return nil
	}
	p := agentstore.MovePending{To: t.ep.Name(), Peer: t.resp.Host.Hostname, RunID: t.m.Transfer, Since: t.mv.Clock.Now()}
	if err := t.mv.Agents.FenceMove(t.rec.Name, p); err != nil {
		return t.recoverable(fmt.Errorf("fencing %s before the commit: %w", t.rec.Name, err))
	}
	return nil
}

// unfence lifts the fence after a settled failure and returns err.
func (t *transfer) unfence(err error) error {
	if t.isCopy() {
		return err
	}
	if uerr := t.mv.Agents.ClearMoveFence(t.rec.Name, t.m.Transfer); uerr != nil {
		return errors.Join(err, fmt.Errorf("lifting %s's move fence: %w; `leo agent move %s --reconcile` lifts it", t.rec.Name, uerr, t.rec.Name))
	}
	return err
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
	return fmt.Errorf("%s is live on %s, but marking it moved here failed: %w; do not start it here: it stays fenced until `leo agent move %s --reconcile` marks it", t.rec.Name, t.ep.Name(), err, t.rec.Name)
}
