# `leo agent worktree` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `leo agent worktree <agent> <branch>` spawns a worktree agent derived from an existing agent — template, repo, and env inherited from its agentstore record; works for any agent whose workspace is a git repo.

**Architecture:** A new `FromAgent` mode on `agent.SpawnSpec`. `Manager.Spawn` routes it to a new `spawnFromAgent`, which resolves the source record and funnels into a `spawnWorktreeCore` extracted (behavior-preserving) from today's `spawnWorktree`. The daemon's `POST /agents/spawn` gains a `from_agent` field; a new CLI subcommand wires it up with the standard remote-host forwarding.

**Tech Stack:** Go, cobra, existing `internal/git` helpers, daemon Unix-socket HTTP API.

**Spec:** `docs/superpowers/specs/2026-07-15-agent-worktree-command-design.md`

## Global Constraints

- All commands run from repo root: `/Users/evan/.leo/agents/leo`
- Tests: `go test -race -run <Name> ./internal/<pkg>/` per task; full `make test` + `make lint` at the end.
- `make e2e` MUST run before push (standing rule: config/argv changes).
- Conventional commits (`feat:`, `refactor:`, `test:`, `docs:`). No AI attribution.
- Follow existing file conventions; new CLI command goes in its own file (agent.go is already >1200 lines).
- Immutability: never mutate a caller's map/slice; `mergeEnv` already returns fresh maps — follow that pattern.

---

### Task 1: `git.HasOrigin` helper

**Files:**
- Modify: `internal/git/worktree.go` (after `Fetch`, ~line 51)
- Test: `internal/git/worktree_test.go`

**Interfaces:**
- Produces: `func HasOrigin(ctx context.Context, repoPath string) bool` — used by Task 4.

- [ ] **Step 1: Write the failing test**

Read `internal/git/worktree_test.go` first. Reuse its existing helpers: `testCtx(t)` and `setupScratchRepo(t)` (returns a clone path and its origin path — check the actual return order in the file). Add:

```go
func TestHasOrigin(t *testing.T) {
	ctx := testCtx(t)
	clone, _ := setupScratchRepo(t) // adjust to the helper's real signature
	if !HasOrigin(ctx, clone) {
		t.Fatal("expected HasOrigin=true for a repo cloned from an origin")
	}

	// A freshly init'd repo has no origin remote.
	bare := t.TempDir()
	cmd := exec.CommandContext(ctx, "git", "init", bare)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if HasOrigin(ctx, bare) {
		t.Fatal("expected HasOrigin=false for a repo with no remotes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestHasOrigin ./internal/git/`
Expected: FAIL — `undefined: HasOrigin`

- [ ] **Step 3: Write minimal implementation**

In `internal/git/worktree.go`, after `Fetch`:

```go
// HasOrigin reports whether the repository at repoPath has an origin remote
// configured. From-agent worktree spawns use this to decide whether a fetch
// (and origin-based default-branch resolution) makes sense.
func HasOrigin(ctx context.Context, repoPath string) bool {
	_, err := ExecGit(ctx, repoPath, "remote", "get-url", "origin")
	return err == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race -run TestHasOrigin ./internal/git/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree.go internal/git/worktree_test.go
git commit -m "feat: add git.HasOrigin helper"
```

---

### Task 2: `ResolveAgentWorktreeLayout`

**Files:**
- Modify: `internal/agent/workspace.go` (after `ResolveWorktreeLayout`)
- Test: `internal/agent/workspace_test.go` (or wherever `ResolveWorktreeLayout` is tested — grep first and colocate)

**Interfaces:**
- Consumes: existing `WorktreeLayout`, `WorktreeRoot`, `git.SlugifyBranch`, `git.BoundedSlug`, `maxBranchSlugInName`.
- Produces: `func ResolveAgentWorktreeLayout(baseWorkspace, canonicalPath, sourceAgent, branch, nameOverride string) (WorktreeLayout, error)` — used by Task 4.

- [ ] **Step 1: Write the failing test**

```go
func TestResolveAgentWorktreeLayout(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/base/chronicle", "chronicle", "a11y", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layout.AgentName != "chronicle-a11y" {
		t.Errorf("AgentName = %q, want chronicle-a11y", layout.AgentName)
	}
	if layout.WorktreePath != "/base/.worktrees/chronicle/a11y" {
		t.Errorf("WorktreePath = %q", layout.WorktreePath)
	}
	if layout.CanonicalPath != "/base/chronicle" {
		t.Errorf("CanonicalPath = %q", layout.CanonicalPath)
	}
	if layout.Branch != "a11y" || layout.BranchSlug != "a11y" {
		t.Errorf("Branch/BranchSlug = %q/%q", layout.Branch, layout.BranchSlug)
	}
}

func TestResolveAgentWorktreeLayout_SlugsBranch(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "feat/new-endpoint", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Same slug scheme as owner/repo worktrees (git.SlugifyBranch).
	if strings.Contains(layout.WorktreePath, "/feat/new-endpoint") {
		t.Errorf("branch was not slugged in path: %q", layout.WorktreePath)
	}
	if layout.AgentName == "" || strings.Contains(layout.AgentName, "/") {
		t.Errorf("bad AgentName %q", layout.AgentName)
	}
}

func TestResolveAgentWorktreeLayout_NameOverride(t *testing.T) {
	layout, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "x", "custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if layout.AgentName != "custom" {
		t.Errorf("AgentName = %q, want custom", layout.AgentName)
	}
}

func TestResolveAgentWorktreeLayout_Errors(t *testing.T) {
	if _, err := ResolveAgentWorktreeLayout("/base", "/c", "", "x", ""); err == nil {
		t.Error("expected error for empty source agent")
	}
	if _, err := ResolveAgentWorktreeLayout("/base", "/c", "leo", "", ""); err == nil {
		t.Error("expected error for empty branch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestResolveAgentWorktreeLayout ./internal/agent/`
Expected: FAIL — `undefined: ResolveAgentWorktreeLayout`

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/workspace.go` after `ResolveWorktreeLayout`:

```go
// ResolveAgentWorktreeLayout computes the layout for a worktree spawned from
// an existing agent (leo agent worktree <agent> <branch>). Unlike
// ResolveWorktreeLayout it keys paths and naming off the source agent's name
// rather than owner/repo, so it works for any git workspace:
//
//	worktree: <base>/.worktrees/<source-agent>/<branch-slug>
//	name:     <source-agent>-<branch-slug>
//
// Pure — no filesystem access; callers validate canonicalPath themselves.
func ResolveAgentWorktreeLayout(baseWorkspace, canonicalPath, sourceAgent, branch, nameOverride string) (WorktreeLayout, error) {
	if sourceAgent == "" {
		return WorktreeLayout{}, fmt.Errorf("from-agent worktree spawn requires a source agent name")
	}
	if branch == "" {
		return WorktreeLayout{}, fmt.Errorf("worktree spawn requires a branch name")
	}
	slug, err := git.SlugifyBranch(branch)
	if err != nil {
		return WorktreeLayout{}, fmt.Errorf("computing branch slug: %w", err)
	}
	name := nameOverride
	if name == "" {
		name = fmt.Sprintf("%s-%s", sourceAgent, git.BoundedSlug(slug, maxBranchSlugInName))
	}
	return WorktreeLayout{
		CanonicalPath: canonicalPath,
		WorktreePath:  filepath.Join(WorktreeRoot(baseWorkspace), sourceAgent, slug),
		Branch:        branch,
		BranchSlug:    slug,
		AgentName:     name,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race -run TestResolveAgentWorktreeLayout ./internal/agent/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/workspace.go internal/agent/workspace_test.go
git commit -m "feat: add ResolveAgentWorktreeLayout for from-agent worktree spawns"
```

---

### Task 3: Extract `spawnWorktreeCore` (behavior-preserving refactor)

**Files:**
- Modify: `internal/agent/manager.go:370-506` (`spawnWorktree`)

**Interfaces:**
- Produces (package-private, used by Task 4):

```go
type worktreeSpawnParams struct {
	baseName   string
	canonical  func() (string, error)
	layout     func(canonical string) (WorktreeLayout, error)
	fetch      bool
	repo       string
	inheritEnv map[string]string
}
func (m *Manager) spawnWorktreeCore(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec, p worktreeSpawnParams) (Record, error)
```

No test-first here — this is a pure refactor gated by the existing suite.

- [ ] **Step 1: Run the existing worktree tests to establish the baseline**

Run: `go test -race ./internal/agent/`
Expected: PASS (record the result)

- [ ] **Step 2: Refactor**

Replace `spawnWorktree` with a thin wrapper + core. The core body is the current function from `reserveUniqueName` onward, with five substitutions (canonical via closure, layout via closure, conditional fetch, `p.repo` persisted, inherited-env merge):

```go
// worktreeSpawnParams carries what spawnWorktreeCore needs beyond the
// SpawnSpec: how to obtain the canonical repo, how to lay out the worktree,
// and what to persist/inherit. Two producers exist — spawnWorktree
// (owner/repo mode) and spawnFromAgent (existing-agent mode).
type worktreeSpawnParams struct {
	// baseName is the derived agent name before -2/-3 collision suffixing.
	baseName string
	// canonical returns the canonical repo path. Called after the agent name
	// is reserved so slow work (cloning) never races a concurrent spawn.
	canonical func() (string, error)
	// layout computes the worktree layout given the canonical path.
	layout func(canonical string) (WorktreeLayout, error)
	// fetch controls whether origin is fetched before the worktree add.
	// False when the canonical has no origin remote.
	fetch bool
	// repo is persisted as the record's Repo. May be empty or a bare name
	// for from-agent spawns.
	repo string
	// inheritEnv is merged between tmpl.Env and spec.Env, minus any key the
	// freshly computed harness env defines — stale harness values from a
	// source agent's record must not leak into the new agent.
	inheritEnv map[string]string
}

func (m *Manager) spawnWorktree(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec) (Record, error) {
	if !strings.Contains(spec.Repo, "/") {
		return Record{}, ErrWorktreeRequiresSlash
	}
	base := BaseWorkspace(tmpl)
	baseName, err := DeriveWorktreeAgentName(spec.Template, spec.Repo, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	return m.spawnWorktreeCore(ctx, cfg, tmpl, spec, worktreeSpawnParams{
		baseName:  baseName,
		canonical: func() (string, error) { return EnsureCanonical(base, spec.Repo) },
		layout: func(canonical string) (WorktreeLayout, error) {
			return ResolveWorktreeLayout(base, canonical, spec.Template, spec.Repo, spec.Branch, spec.Name)
		},
		fetch: true,
		repo:  spec.Repo,
	})
}

func (m *Manager) spawnWorktreeCore(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec, p worktreeSpawnParams) (Record, error) {
	agentName, err := m.reserveUniqueName(p.baseName)
	if err != nil {
		return Record{}, err
	}
	// ... [current body of spawnWorktree unchanged from here] ...
}
```

Inside the moved body make exactly these substitutions:

1. `canonical, err := EnsureCanonical(base, spec.Repo)` → `canonical, err := p.canonical()`
2. `layout, err := ResolveWorktreeLayout(base, canonical, ...)` → `layout, err := p.layout(canonical)`
3. Wrap the fetch block:

```go
	if p.fetch {
		fetchCtx, cancel := context.WithTimeout(ctx, gitFetchTimeout)
		defer cancel()
		if err := git.Fetch(fetchCtx, canonical); err != nil {
			return Record{}, fmt.Errorf("fetching origin: %w", err)
		}
	}
```

4. Env merge (replaces `env := mergeEnv(mergeEnv(harnessEnv, tmpl.Env), spec.Env)`):

```go
	inherited := p.inheritEnv
	if len(inherited) > 0 && len(harnessEnv) > 0 {
		pruned := make(map[string]string, len(inherited))
		for k, v := range inherited {
			if _, fresh := harnessEnv[k]; !fresh {
				pruned[k] = v
			}
		}
		inherited = pruned
	}
	env := mergeEnv(mergeEnv(mergeEnv(harnessEnv, tmpl.Env), inherited), spec.Env)
```

5. Both the `agentstore.Record{...}` literal and the returned `Record{...}` literal: `Repo: spec.Repo` → `Repo: p.repo`.

Everything else (name reservation/release, rollback, persist-before-spawn ordering, session-id handling) stays byte-identical — those comments explain load-bearing ordering; do not reorder.

- [ ] **Step 3: Run the full agent package suite**

Run: `go test -race ./internal/agent/`
Expected: PASS — identical to baseline. Also run `go vet ./internal/agent/`.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/manager.go
git commit -m "refactor: extract spawnWorktreeCore from spawnWorktree"
```

---

### Task 4: `SpawnSpec.FromAgent` + `spawnFromAgent`

**Files:**
- Modify: `internal/agent/manager.go` (SpawnSpec ~line 75, `Spawn` ~line 155, new funcs after `spawnWorktree`)
- Modify: `internal/agent/errors.go`
- Test: `internal/agent/manager_worktree_test.go`

**Interfaces:**
- Consumes: `ResolveAgentWorktreeLayout` (Task 2), `git.HasOrigin` (Task 1), `spawnWorktreeCore` (Task 3), `agentstore.Load/FilePath/Save`, `NormalizeAgentName`.
- Produces (used by Task 5):
  - `SpawnSpec.FromAgent string` field
  - `var ErrSourceAgentNotFound = errors.New("source agent not found")`
  - `var ErrSourceNotGitRepo = errors.New("source agent's workspace is not a git repository")`

- [ ] **Step 1: Write the failing tests**

Read `internal/agent/manager_worktree_test.go` FIRST and mirror its fixtures exactly — it already has a fake supervisor, a config loader seam, and temp-git-repo setup for worktree spawn tests. Reuse those helpers verbatim (same fake supervisor type, same way of seeding `cfg.Templates` and `HomePath`). Add tests covering:

```go
// Adapt helper names to what manager_worktree_test.go actually provides.

func TestSpawnFromAgent_LocalRepoNoRemote(t *testing.T) {
	// Arrange: temp HomePath; a source git repo with one commit and NO
	// remotes (git init + commit); an agentstore record for it:
	//   agentstore.Save(home, agentstore.Record{
	//       Name: "chronicle", Template: "claude",
	//       Workspace: srcRepo,
	//       Env: map[string]string{"FOO": "bar"},
	//   })
	// and cfg.Templates["claude"] existing.

	// Act:
	rec, err := mgr.Spawn(ctx, SpawnSpec{FromAgent: "chronicle", Branch: "a11y"})

	// Assert:
	// err == nil
	// rec.Name == "chronicle-a11y"
	// rec.Template == "claude"
	// rec.Branch == "a11y"
	// rec.CanonicalPath == srcRepo
	// rec.Workspace == filepath.Join(base, ".worktrees", "chronicle", "a11y")
	// rec.Env["FOO"] == "bar"                    // inherited
	// worktree dir exists on disk with .git file
	// persisted store record has same fields (agentstore.Load)
}

func TestSpawnFromAgent_WorktreeSourceUsesCanonical(t *testing.T) {
	// Arrange: source record has CanonicalPath set (simulating a worktree
	// agent): Workspace = some subdir, CanonicalPath = the real git repo.
	// Act: Spawn(FromAgent: source, Branch: "fix-2")
	// Assert: rec.CanonicalPath == the record's CanonicalPath, spawn succeeds.
}

func TestSpawnFromAgent_EnvOverridesInherited(t *testing.T) {
	// Source env {"FOO": "bar", "KEEP": "1"}; spec.Env {"FOO": "override"}.
	// Assert rec.Env["FOO"] == "override" && rec.Env["KEEP"] == "1".
}

func TestSpawnFromAgent_SourceNotFound(t *testing.T) {
	_, err := mgr.Spawn(ctx, SpawnSpec{FromAgent: "ghost", Branch: "x"})
	// errors.Is(err, ErrSourceAgentNotFound)
}

func TestSpawnFromAgent_SourceNotGitRepo(t *testing.T) {
	// Record whose Workspace is a plain directory (no .git).
	// errors.Is(err, ErrSourceNotGitRepo)
}

func TestSpawnFromAgent_RequiresBranch(t *testing.T) {
	_, err := mgr.Spawn(ctx, SpawnSpec{FromAgent: "chronicle"})
	// err != nil, mentions branch
}

func TestSpawnFromAgent_RejectsExplicitTemplateOrRepo(t *testing.T) {
	_, err := mgr.Spawn(ctx, SpawnSpec{FromAgent: "chronicle", Branch: "x", Template: "claude"})
	// err != nil
	_, err = mgr.Spawn(ctx, SpawnSpec{FromAgent: "chronicle", Branch: "x", Repo: "a/b"})
	// err != nil
}
```

Write these as real tests (full Arrange code), not comments — the comments above define the required assertions.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestSpawnFromAgent ./internal/agent/`
Expected: FAIL — `unknown field FromAgent`, `undefined: ErrSourceAgentNotFound`

- [ ] **Step 3: Implement**

**errors.go** — add to the existing var block:

```go
	// ErrSourceAgentNotFound is returned by from-agent worktree spawns when
	// the named source agent has no agentstore record.
	ErrSourceAgentNotFound = errors.New("source agent not found")

	// ErrSourceNotGitRepo is returned by from-agent worktree spawns when the
	// source agent's workspace is not a git repository — there is nothing to
	// add a worktree to.
	ErrSourceNotGitRepo = errors.New("source agent's workspace is not a git repository")
```

**manager.go** — `SpawnSpec`, add after `Template`:

```go
	// FromAgent, when non-empty, derives the spawn from an existing agent's
	// record: template and env are inherited, and the source workspace (or
	// canonical, for worktree sources) becomes the git canonical. Requires
	// Branch; Template and Repo must be empty. Works for any agent whose
	// workspace is a git repository — no owner/repo needed.
	FromAgent string
```

**manager.go** — `Spawn` routing (replace the current body's opening):

```go
func (m *Manager) Spawn(ctx context.Context, spec SpawnSpec) (Record, error) {
	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	if spec.FromAgent != "" {
		if spec.Template != "" || spec.Repo != "" {
			return Record{}, fmt.Errorf("from-agent spawn derives template and repo from the source agent; do not set them")
		}
		return m.spawnFromAgent(ctx, cfg, spec)
	}
	if spec.Template == "" {
		return Record{}, fmt.Errorf("template is required")
	}
	tmpl, ok := cfg.Templates[spec.Template]
	if !ok {
		return Record{}, fmt.Errorf("template %q not found", spec.Template)
	}
	return m.spawnResolved(ctx, cfg, tmpl, spec)
}
```

**manager.go** — new funcs after `spawnWorktree`:

```go
// lookupStored returns the raw agentstore record for query, trying the query
// verbatim and its normalized form. Mirrors resolveStored but returns the
// stored record (env, canonical path) rather than the public Record view.
func lookupStored(homePath, query string) (agentstore.Record, bool) {
	stored, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return agentstore.Record{}, false
	}
	candidates := []string{strings.TrimSpace(query)}
	if norm, err := NormalizeAgentName(query); err == nil {
		candidates = append(candidates, norm)
	}
	for _, name := range candidates {
		if rec, ok := stored[name]; ok {
			if rec.Name == "" {
				rec.Name = name
			}
			return rec, true
		}
	}
	return agentstore.Record{}, false
}

// spawnFromAgent spawns a worktree agent derived from an existing agent's
// record. Unlike spawnWorktree there is no owner/repo requirement: the
// source agent's workspace (or its canonical, for worktree sources) is the
// git canonical, fetch is skipped for remoteless repos, and new branches on
// remoteless repos fall back to HEAD as their base ref.
func (m *Manager) spawnFromAgent(ctx context.Context, cfg *config.Config, spec SpawnSpec) (Record, error) {
	if spec.Branch == "" {
		return Record{}, fmt.Errorf("worktree spawn requires a branch name")
	}
	src, ok := lookupStored(cfg.HomePath, spec.FromAgent)
	if !ok {
		return Record{}, fmt.Errorf("%w: %q (run 'leo agent list')", ErrSourceAgentNotFound, spec.FromAgent)
	}
	tmpl, ok := cfg.Templates[src.Template]
	if !ok {
		return Record{}, fmt.Errorf("template %q (from agent %q) not found", src.Template, src.Name)
	}
	canonical := src.CanonicalPath
	if canonical == "" {
		canonical = src.Workspace
	}
	if _, err := os.Stat(filepath.Join(canonical, ".git")); err != nil {
		return Record{}, fmt.Errorf("%w: %s", ErrSourceNotGitRepo, canonical)
	}
	hasOrigin := git.HasOrigin(ctx, canonical)
	if !hasOrigin && spec.Base == "" {
		// AddWorktreeForBranch resolves origin's default branch when no base
		// is given; remoteless repos have no origin, so branch off HEAD.
		spec.Base = "HEAD"
	}
	spec.Template = src.Template

	base := BaseWorkspace(tmpl)
	layout, err := ResolveAgentWorktreeLayout(base, canonical, src.Name, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	return m.spawnWorktreeCore(ctx, cfg, tmpl, spec, worktreeSpawnParams{
		baseName:  layout.AgentName,
		canonical: func() (string, error) { return canonical, nil },
		layout: func(string) (WorktreeLayout, error) {
			return ResolveAgentWorktreeLayout(base, canonical, src.Name, spec.Branch, spec.Name)
		},
		fetch:      hasOrigin,
		repo:       src.Repo,
		inheritEnv: src.Env,
	})
}
```

Note: `spec` is a value copy — assigning `spec.Base`/`spec.Template` does not mutate the caller's struct. Add `io/fs` to imports if not present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/agent/`
Expected: PASS (new tests and the whole package)

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go internal/agent/errors.go internal/agent/manager_worktree_test.go
git commit -m "feat: spawn worktree agents from an existing agent (SpawnSpec.FromAgent)"
```

---

### Task 5: Daemon API — `from_agent` on POST /agents/spawn

**Files:**
- Modify: `internal/daemon/types.go:48-66` (`AgentSpawnRequest`)
- Modify: `internal/daemon/handlers_agents.go:37-75` (`handleAgentSpawn`), `writeAgentError` (~line 466)
- Test: `internal/daemon/handlers_agents_test.go`

**Interfaces:**
- Consumes: `agent.SpawnSpec.FromAgent`, `agent.ErrSourceAgentNotFound`, `agent.ErrSourceNotGitRepo` (Task 4).
- Produces: `AgentSpawnRequest.FromAgent string` (json `from_agent`) — used by Task 6. New error codes following the existing `ErrorCode*` constant pattern (grep `ErrorCodeWorktreeRequireSep` for the block): `ErrorCodeSourceAgentNotFound = "source_agent_not_found"`, `ErrorCodeSourceNotGitRepo = "source_not_git_repo"`.

- [ ] **Step 1: Write the failing tests**

Read the existing spawn handler tests at `internal/daemon/handlers_agents_test.go:160-260` and mirror their fixture (fake manager / test server). Add:

```go
// 1. POST /agents/spawn with {"from_agent":"chronicle","branch":"a11y"} and
//    no template → handler passes SpawnSpec{FromAgent:"chronicle", Branch:"a11y"}
//    to the manager (assert via the fake's captured spec) and returns 200.
// 2. POST with neither template nor from_agent → 400 "template or from_agent is required".
// 3. Fake manager returns agent.ErrSourceAgentNotFound → 404 with code
//    "source_agent_not_found".
// 4. Fake manager returns agent.ErrSourceNotGitRepo → 400 with code
//    "source_not_git_repo".
```

Write real tests following the file's existing table/request style.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestHandleAgentSpawn ./internal/daemon/`
(adjust -run to the actual test names in that file)
Expected: FAIL — unknown field `from_agent` behavior / 400 on missing template

- [ ] **Step 3: Implement**

`types.go`, add to `AgentSpawnRequest`:

```go
	// FromAgent, when set, derives the spawn from an existing agent's record
	// (template, repo, env inherited; its workspace is the git canonical).
	// Requires Branch; Template and Repo must be empty.
	FromAgent string `json:"from_agent,omitempty"`
```

`handlers_agents.go`, in `handleAgentSpawn` replace the template check and spec literal:

```go
	if req.Template == "" && req.FromAgent == "" {
		writeError(w, http.StatusBadRequest, "template or from_agent is required")
		return
	}

	rec, err := s.agentMgr.Spawn(r.Context(), agent.SpawnSpec{
		Template:    req.Template,
		FromAgent:   req.FromAgent,
		Repo:        req.Repo,
		Name:        req.Name,
		Branch:      req.Branch,
		Base:        req.Base,
		Prompt:      req.Prompt,
		Env:         req.Env,
		IdleSuspend: req.IdleSuspend,
	})
```

`writeAgentError`, add before the default case (and add the two `ErrorCode*` constants beside the existing ones):

```go
	case errors.Is(err, agent.ErrSourceAgentNotFound):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeSourceAgentNotFound})
	case errors.Is(err, agent.ErrSourceNotGitRepo):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeSourceNotGitRepo})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/daemon/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/types.go internal/daemon/handlers_agents.go internal/daemon/handlers_agents_test.go
git commit -m "feat: accept from_agent on POST /agents/spawn"
```

---

### Task 6: CLI — `leo agent worktree <agent> <branch>`

**Files:**
- Create: `internal/cli/agent_worktree.go`
- Create: `internal/cli/agent_worktree_test.go`
- Modify: `internal/cli/agent.go:59-73` (register in `newAgentCmd`'s `AddCommand` list, after `newAgentSpawnCmd()`)

**Interfaces:**
- Consumes: `daemon.AgentSpawn` + `AgentSpawnRequest.FromAgent` (Task 5), existing CLI helpers `dispatch`, `runRemote`, `addHostFlag`, `parseEnvPairs`, `agentStdout`.

- [ ] **Step 1: Write the failing tests**

Read `internal/cli/agent_test.go` first and mirror how spawn tests stub the daemon/dispatch seams. Cover:

```go
// 1. Local dispatch: `leo agent worktree chronicle a11y --base main --env K=V`
//    sends AgentSpawnRequest{FromAgent:"chronicle", Branch:"a11y", Base:"main",
//    Env:{"K":"V"}} and prints "spawned <name> (branch: ...)" + attach hint.
// 2. --json prints the record as indented JSON.
// 3. Remote dispatch (--host with a configured host): forwards
//    ["worktree", "chronicle", "a11y", "--base", "main", ...] via runRemote
//    (assert with the same seam the spawn remote test uses).
// 4. Arg validation: exactly 2 args required (cobra.ExactArgs).
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestAgentWorktree ./internal/cli/`
Expected: FAIL — `undefined: newAgentWorktreeCmd`

- [ ] **Step 3: Implement**

`internal/cli/agent_worktree.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// newAgentWorktreeCmd spawns a worktree agent branched off an existing
// agent — sugar over spawn --worktree that derives template, repo, and env
// from the source agent's record and works for any git workspace.
func newAgentWorktreeCmd() *cobra.Command {
	var (
		host     string
		name     string
		base     string
		prompt   string
		envPairs []string
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "worktree <agent> <branch>",
		Short: "Spawn a worktree agent branched off an existing agent",
		Long: `Spawn a new agent in a dedicated git worktree derived from an existing
agent: the source agent's template and env are inherited, and its workspace
serves as the git canonical. Works for any agent whose workspace is a git
repository — no owner/repo required. Branching off a worktree agent uses its
canonical repo; pass --base <its-branch> to fork from that branch.

The new agent is named <agent>-<branch-slug> and its worktree lives under
<workspace>/.worktrees/<agent>/<branch-slug>. Clean up with
'leo agent stop <name> --prune'.`,
		Example: `  # Branch the chronicle agent onto an a11y worktree
  leo agent worktree chronicle a11y

  # Fork off a specific ref with an opening prompt
  leo agent worktree chronicle hotfix --base v1.2.0 --prompt "fix the crash"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceAgent, branch := args[0], args[1]
			env, err := parseEnvPairs(envPairs)
			if err != nil {
				return err
			}
			cfg, res, err := dispatch(host)
			if err != nil {
				return err
			}
			if !res.Localhost {
				extra := []string{"worktree", sourceAgent, branch}
				if asJSON {
					extra = append(extra, "--json")
				}
				if name != "" {
					extra = append(extra, "--name", name)
				}
				if base != "" {
					extra = append(extra, "--base", base)
				}
				if prompt != "" {
					extra = append(extra, "--prompt", prompt)
				}
				for _, p := range envPairs {
					extra = append(extra, "--env", p)
				}
				return runRemote(res, extra)
			}

			rec, err := daemon.AgentSpawn(cmd.Context(), cfg.HomePath, daemon.AgentSpawnRequest{
				FromAgent: sourceAgent,
				Branch:    branch,
				Base:      base,
				Name:      name,
				Prompt:    prompt,
				Env:       env,
			})
			if err != nil {
				return fmt.Errorf("spawning worktree agent: %w", err)
			}
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rec)
			}
			fmt.Fprintf(agentStdout, "spawned %s (branch: %s, worktree: %s)\n", rec.Name, rec.Branch, rec.Workspace)
			fmt.Fprintf(agentStdout, "attach with: leo agent attach %s\n", rec.Name)
			return nil
		},
	}
	addHostFlag(cmd, &host)
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the spawned agent record as JSON")
	cmd.Flags().StringVar(&name, "name", "", "override the derived agent name")
	cmd.Flags().StringVar(&base, "base", "", "base ref for new branches (defaults to origin HEAD, or HEAD for remoteless repos)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "opening prompt delivered as the agent's first interactive turn")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "extra env var as KEY=VALUE (repeatable); overrides inherited env on collision")
	return cmd
}
```

(Verify the remote-forwarding argv prefix against how spawn builds `extra` — it starts with the subcommand name, `runRemote` adds the `leo agent` prefix.)

Register in `internal/cli/agent.go`:

```go
	cmd.AddCommand(
		newAgentListCmd(),
		newAgentSpawnCmd(),
		newAgentWorktreeCmd(),
		newAgentAttachCmd(),
		...
	)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/cli/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli/agent_worktree.go internal/cli/agent_worktree_test.go internal/cli/agent.go
git commit -m "feat: leo agent worktree — spawn a worktree agent from an existing agent"
```

---

### Task 7: Docs + full verification

**Files:**
- Modify: `docs/cli/agent.md` (add a `worktree` subcommand section next to `spawn`; follow the file's existing per-subcommand format)
- Modify: `docs/guides/agents.md` (where worktree spawning is explained, add the from-agent shorthand; `grep -n worktree docs/guides/agents.md` to find the spot)

**Interfaces:** none — documentation and verification only.

- [ ] **Step 1: Update docs**

Document: synopsis, what is inherited (template, env; repo recorded), naming (`<agent>-<branch-slug>`), worktree location, remoteless-repo behavior (no fetch, branches off HEAD), branching off a worktree agent (canonical + `--base`), flags, cleanup via `stop --prune`, and a chronicle example. Match the surrounding docs' voice and heading depth.

- [ ] **Step 2: Full verification**

```bash
make test    # go test -race -cover ./...
make lint    # mirrors CI (golangci-lint + gosec)
make e2e     # MANDATORY — argv/CLI changes (standing rule)
```

Expected: all PASS. Note: one interrupt test is known-flaky on CI — rerun before debugging (memory: project_flaky_interrupt_test).

- [ ] **Step 3: Commit**

```bash
git add docs/cli/agent.md docs/guides/agents.md
git commit -m "docs: document leo agent worktree"
```

- [ ] **Step 4: Manual smoke test (optional but cheap)**

```bash
make build
./bin/leo agent worktree --help
```

Expected: help text renders with all flags.
