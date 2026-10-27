package agentmove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// Destination verbs, served by `leo agent move-receive <verb>` over ssh (and
// in-process for a local copy). Each takes the manifest as its body.
const (
	VerbPreflight  = "preflight"
	VerbCommit     = "commit"
	VerbStatus     = "status"
	VerbGitSeed    = "git-seed"
	VerbGitPrepare = "git-prepare"
)

// Verbs lists every destination verb.
var Verbs = []string{VerbPreflight, VerbCommit, VerbStatus, VerbGitSeed, VerbGitPrepare}

// CodeBadRequest refuses a verb whose body is not a usable manifest.
const CodeBadRequest = "bad_request"

// DefaultStartConfirm is how long a received agent must stay up before the
// commit reports it live.
const DefaultStartConfirm = 15 * time.Second

// StagingDir is an agent's move staging dir under a leo home: hooks/ and
// harness/ travel from source to destination through it.
func StagingDir(homePath, name string) string {
	return filepath.Join(homePath, "state", "moves", name, "staging")
}

// ReceiveManager is the part of agent.Manager a destination needs.
type ReceiveManager interface {
	List() []agent.Record
	Receive(ctx context.Context, spec agent.ReceiveSpec) error
	ShadowReceive(spec agent.ReceiveSpec, cause error) error
}

// Clock lets tests skip the start confirmation wait.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Receiver serves the destination verbs.
type Receiver struct {
	Config       func() (*config.Config, error)
	Agents       ReceiveManager
	Hooks        HookRunner
	Runner       Runner
	Clock        Clock
	Host         func() (Host, error)
	LookPath     func(string) (string, error)
	Version      string
	Locks        *KeyedLocks
	StartConfirm time.Duration
}

// NewReceiver returns a Receiver with the real hooks, exec, clock and host.
func NewReceiver(cfg func() (*config.Config, error), agents ReceiveManager, version string) *Receiver {
	return &Receiver{
		Config: cfg, Agents: agents, Hooks: ExecHooks{}, Runner: ExecRunner{}, Clock: realClock{},
		Host: LocalHost, LookPath: exec.LookPath, Version: version, Locks: &KeyedLocks{},
		StartConfirm: DefaultStartConfirm,
	}
}

// LocalHost identifies this host: hostname, home and user.
func LocalHost() (Host, error) {
	name, err := os.Hostname()
	if err != nil {
		return Host{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Host{}, err
	}
	u, err := user.Current()
	if err != nil {
		return Host{}, err
	}
	return Host{Hostname: name, Home: home, User: u.Username}, nil
}

// Handle serves one verb: body is the manifest, the result is the verb's
// JSON answer.
func (r *Receiver) Handle(ctx context.Context, verb string, body []byte) ([]byte, error) {
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, problemf(CodeBadRequest, "decoding the manifest: %v", err)
	}
	var (
		out any
		err error
	)
	switch verb {
	case VerbPreflight:
		out, err = r.Preflight(ctx, m)
	case VerbStatus:
		out, err = r.Status(m)
	case VerbCommit, VerbGitSeed, VerbGitPrepare:
		if err := validateManifest(m); err != nil {
			return nil, err
		}
		out, err = r.mutate(ctx, verb, m)
	default:
		return nil, problemf(CodeBadRequest, "unknown move verb %q", verb)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (r *Receiver) mutate(ctx context.Context, verb string, m Manifest) (any, error) {
	switch verb {
	case VerbCommit:
		return r.Commit(ctx, m)
	case VerbGitSeed:
		if m.Git == nil || m.Git.Repo == "" {
			return nil, problemf(CodeCanonicalMissing, "nothing to clone %s from", m.TargetWorkspace())
		}
		return struct{}{}, SeedCanonical(ctx, r.Runner, m.Git.Repo, m.Git.Canonical)
	default:
		return GitPrepare(ctx, m, r.Locks)
	}
}

// validateManifest checks every name and path a mutating verb acts on.
func validateManifest(m Manifest) error {
	if m.Protocol != Protocol {
		return problemf(CodeProtocolMismatch, "move protocol %d, want %d", m.Protocol, Protocol)
	}
	if m.Mode == ModeCopy && m.Copy == nil {
		return problemf(CodeBadRequest, "a copy manifest needs its copy identity")
	}
	if err := ValidateRef(m.TargetName()); err != nil {
		return err
	}
	if err := ValidatePath(m.TargetWorkspace()); err != nil {
		return err
	}
	if b := m.TargetBranch(); b != "" {
		if err := ValidateRef(b); err != nil {
			return err
		}
	}
	if m.gitMode() {
		return ValidatePath(m.Git.Canonical)
	}
	return nil
}

// Preflight gathers this host's facts for m and judges them.
func (r *Receiver) Preflight(ctx context.Context, m Manifest) (PreflightResponse, error) {
	cfg, err := r.Config()
	if err != nil {
		return PreflightResponse{}, fmt.Errorf("loading config: %w", err)
	}
	host, err := r.Host()
	if err != nil {
		return PreflightResponse{}, err
	}
	d := r.facts(ctx, cfg, host, m)
	problems := CheckDestination(m, d)
	return PreflightResponse{
		Protocol: Protocol, Host: host, LeoVersion: r.Version, OK: len(problems) == 0, Problems: problems,
		SessionRoot: d.SessionRoot, StagingDir: d.StagingDir, Existing: d.Existing, WSState: d.WSState, Git: d.Git,
	}, nil
}

func (r *Receiver) facts(ctx context.Context, cfg *config.Config, host Host, m Manifest) DestFacts {
	d := DestFacts{Host: host, StagingDir: StagingDir(cfg.HomePath, m.TargetName())}
	tmpl, found := cfg.Templates[m.Agent.Template]
	d.TemplateFound = found
	if found {
		d.TemplateHarness = cfg.TemplateHarness(tmpl)
	}
	names := cfg.EnvironmentNames(m.Agent.Environments, tmpl.Environments)
	for _, n := range names {
		if _, ok := cfg.Environments[n]; !ok {
			d.MissingEnvironments = append(d.MissingEnvironments, n)
		}
	}
	if h, err := harness.Get(orClaude(m.Agent.Harness)); err == nil {
		if p, err := r.LookPath(h.Binary()); err == nil {
			d.HarnessBinary = p
		}
		if mover, ok := h.(harness.SessionMover); ok && len(d.MissingEnvironments) == 0 {
			if root := mover.SessionRoot(agentEnv(cfg, tmpl, m)); root != "" {
				d.SessionRoot, d.SessionRootPresent = root, isDir(root)
			}
		}
	}
	d.Existing, d.WorkspaceUsers = r.existing(m)
	d.Git = r.gitFacts(ctx, m)
	d.WSState = wsState(ctx, m, d.Existing)
	return d
}

// agentEnv is the env the agent will run with here, as far as config says.
func agentEnv(cfg *config.Config, tmpl config.TemplateConfig, m Manifest) map[string]string {
	env, err := cfg.ResolveEnv(cfg.EnvironmentNames(m.Agent.Environments, tmpl.Environments), tmpl.Env, m.Agent.SpawnEnv, config.Expander{})
	if err != nil {
		return nil
	}
	return env
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// existing finds the target name's record and other agents on the target
// workspace.
func (r *Receiver) existing(m Manifest) (Existing, []string) {
	var ex Existing
	var users []string
	for _, a := range r.Agents.List() {
		switch {
		case a.Name == m.TargetName():
			ex = Existing{Present: true, MovedTo: a.MovedTo, Live: a.Status == "running"}
		case a.Workspace != "" && git.SamePath(a.Workspace, m.TargetWorkspace()):
			users = append(users, a.Name)
		}
	}
	return ex, users
}

func (r *Receiver) gitFacts(ctx context.Context, m Manifest) *GitPreflight {
	if m.Git == nil {
		return nil
	}
	gp := &GitPreflight{}
	gp.GitPath, _ = r.LookPath("git")
	canonical := m.Git.Canonical
	if canonical == "" {
		canonical = m.TargetWorkspace()
	}
	if gp.GitPath == "" {
		return gp
	}
	if !isDir(filepath.Join(canonical, ".git")) {
		_, ghErr := r.LookPath("gh")
		owner, name, ok := strings.Cut(m.Git.Repo, "/")
		gp.Cloneable = ghErr == nil && ok && owner != "" && name != ""
		return gp
	}
	gp.CanonicalPresent = true
	for _, root := range m.Git.RootSHAs {
		if git.HasCommit(ctx, canonical, root) {
			gp.HasRoot = true
			break
		}
	}
	if b := m.TargetBranch(); b != "" {
		gp.BranchSHA, _ = git.RefSHA(ctx, canonical, "refs/heads/"+b)
	}
	if list, err := git.WorktreeList(ctx, canonical); err == nil {
		gp.CheckedOut = git.CheckedOut(list)
		gp.LinkedWorktrees = len(list) - 1
	}
	if marker, err := git.OpInProgress(ctx, canonical); err == nil && marker != "" {
		gp.OpInProgress = opNames[marker]
	}
	return gp
}

// wsState classifies what sits at the target workspace (see WSAbsent).
func wsState(ctx context.Context, m Manifest, ex Existing) string {
	ws := m.TargetWorkspace()
	entries, err := os.ReadDir(ws)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0) {
		return WSAbsent
	}
	mine := !ex.Present || ex.MovedTo != ""
	switch {
	case m.IsCopy():
		return WSForeign
	case m.gitMode() && m.Git.Mode == GitModeLinked:
		if list, err := git.WorktreeList(ctx, m.Git.Canonical); err == nil && registered(list, ws) && mine {
			return WSOurs
		}
		return WSForeign
	case m.gitMode() && mine:
		return WSOurs
	case !m.gitMode() && ex.Present && ex.MovedTo != "":
		return WSOurs
	}
	return WSForeign
}

// CommitResult answers a commit.
type CommitResult struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Notes  []Problem `json:"notes,omitempty"`
}

// Commit lands the transfer (S6): git finalize (or a plain copy's
// placement), the harness's FinishReceive, post_receive hooks, then the
// record and start, confirmed for StartConfirm. Any failure leaves the
// agent dormant (agent.Manager.ShadowReceive).
func (r *Receiver) Commit(ctx context.Context, m Manifest) (CommitResult, error) {
	cfg, err := r.Config()
	if err != nil {
		return CommitResult{}, fmt.Errorf("loading config: %w", err)
	}
	host, err := r.Host()
	if err != nil {
		return CommitResult{}, err
	}
	spec := receiveSpec(m, r.Clock.Now())
	fail := func(err error) (CommitResult, error) {
		if serr := r.Agents.ShadowReceive(spec, err); serr != nil {
			return CommitResult{}, errors.Join(err, fmt.Errorf("leaving it dormant: %w", serr))
		}
		return CommitResult{}, err
	}
	notes, err := r.place(ctx, m)
	if err != nil {
		return fail(err)
	}
	tmpl := cfg.Templates[m.Agent.Template]
	env := agentEnv(cfg, tmpl, m)
	if err := r.finishHarness(ctx, cfg, host, m, env); err != nil {
		return fail(err)
	}
	mv := cfg.ResolveMove(m.Agent.Template)
	if err := r.Hooks.RunHooks(ctx, "post_receive", mv.PostReceive, mv.HookTimeout, r.hookEnv(cfg, host, m, env)); err != nil {
		return fail(err)
	}
	if err := r.Agents.Receive(ctx, spec); err != nil {
		return CommitResult{}, err
	}
	if err := r.confirm(ctx, spec.Record.Name); err != nil {
		return fail(err)
	}
	return CommitResult{Name: spec.Record.Name, Status: "running", Notes: notes}, nil
}

// place finishes the workspace: G4 in git mode, a plain copy's rename and
// branch otherwise.
func (r *Receiver) place(ctx context.Context, m Manifest) ([]Problem, error) {
	if m.gitMode() {
		return GitFinalize(ctx, m, r.Locks)
	}
	if !m.IsCopy() {
		return nil, nil
	}
	partial, ws := m.TargetWorkspace()+PartialSuffix, m.TargetWorkspace()
	if _, err := os.Stat(partial); err == nil {
		if err := os.Rename(partial, ws); err != nil {
			return nil, fmt.Errorf("placing the copy's workspace: %w", err)
		}
	}
	return nil, ForkPlainBranch(ctx, ws, m.Copy.Branch, m.Copy.Name)
}

func (r *Receiver) finishHarness(ctx context.Context, cfg *config.Config, host Host, m Manifest, env map[string]string) error {
	h, err := harness.Get(orClaude(m.Agent.Harness))
	if err != nil {
		return err
	}
	mover, ok := h.(harness.SessionMover)
	if !ok {
		return problemf(CodeHarnessUnsupported, "%s cannot receive a moved session", h.Name())
	}
	mc := harness.MoveContext{
		Env: env, Workspace: m.TargetWorkspace(), SessionIDs: []string{m.SourceSessionID},
		Copy: m.IsCopy(), Local: m.From.Hostname == host.Hostname,
	}
	return mover.FinishReceive(ctx, mc, StagingDir(cfg.HomePath, m.TargetName()))
}

func (r *Receiver) hookEnv(cfg *config.Config, host Host, m Manifest, env map[string]string) HookEnv {
	sid := m.SourceSessionID
	if m.IsCopy() && m.Copy.SessionID != "" {
		sid = m.Copy.SessionID
	}
	he := HookEnv{
		Agent: m.TargetName(), Workspace: m.TargetWorkspace(), SessionID: sid,
		From: m.From.Hostname, To: host.Hostname, Dir: filepath.Join(StagingDir(cfg.HomePath, m.TargetName()), "hooks"),
		Mode: m.Mode, SourceAgent: m.Agent.Name, SourceWorkspace: m.Agent.Workspace, SourceSessionID: m.SourceSessionID,
		AgentEnv: env,
	}
	if m.LastMove != nil {
		he.Last = *m.LastMove
	}
	return he
}

// receiveSpec is the agent as it lands here: a copy under its new identity
// (a git-mode copy becomes a linked worktree of the canonical).
func receiveSpec(m Manifest, at time.Time) agent.ReceiveSpec {
	rec := m.Agent.Portable()
	spec := agent.ReceiveSpec{Record: rec, From: m.From.Hostname, At: at}
	if !m.IsCopy() {
		if m.SourceSessionID != "" {
			spec.Record.SessionID = m.SourceSessionID
		}
		return spec
	}
	spec.Copy, spec.ForkFrom = true, m.SourceSessionID
	spec.Record.Name, spec.Record.Workspace, spec.Record.SessionID = m.Copy.Name, m.Copy.Workspace, m.Copy.SessionID
	if m.gitMode() {
		spec.Record.Branch, spec.Record.CanonicalPath = m.Copy.Branch, m.Git.Canonical
	}
	return spec
}

// confirm waits StartConfirm, then requires the agent running with no
// restarts: a quick exit is a failed start.
func (r *Receiver) confirm(ctx context.Context, name string) error {
	select {
	case <-r.Clock.After(r.StartConfirm):
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, a := range r.Agents.List() {
		if a.Name != name {
			continue
		}
		if a.Status == "running" && a.Restarts == 0 {
			return nil
		}
		return fmt.Errorf("%s did not stay up after starting (status %s, %d restarts)", name, a.Status, a.Restarts)
	}
	return fmt.Errorf("%s vanished after starting", name)
}

// StatusResult answers a status query: the target name's state here.
type StatusResult struct {
	Present bool   `json:"present"`
	MovedTo string `json:"moved_to,omitempty"`
	Status  string `json:"status,omitempty"`
	Live    bool   `json:"live"`
}

// Status reports the target agent's state, for a source whose commit
// outcome is unknown.
func (r *Receiver) Status(m Manifest) (StatusResult, error) {
	for _, a := range r.Agents.List() {
		if a.Name == m.TargetName() {
			return StatusResult{Present: true, MovedTo: a.MovedTo, Status: a.Status, Live: a.Status == "running"}, nil
		}
	}
	return StatusResult{}, nil
}
