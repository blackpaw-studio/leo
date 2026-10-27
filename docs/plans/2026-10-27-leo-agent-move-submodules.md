# Amendment: submodules in v1

Amends `2026-10-27-leo-agent-move-worktrees.md`. Each block names the section it replaces or extends. Order of precedence: spec > this file > worktrees amendment > base plan.

## Orchestrator decisions (2026-10-27)
- A copy forks submodule branches: a submodule on `<b>` gets `<b>-<new>`, and a detached one stays detached. This matches the spec rule that two agents never push to the same branch.
- Nested foreign worktrees are untracked `.git` *files* inside the workspace that are not absorbed submodules of this repo, e.g. Claude Code worktrees under `.claude/worktrees/`. They are excluded from the workspace rsync with a non-fatal `nested_worktree_skipped` note naming each path. They are not refused, because that would block `leoterm`. This is in v1 scope, not a follow-up.

### §0, extends decision 1 (Evan, 2026-10-27: submodules work in v1)
- **Submodule git dirs are private to one worktree.** `git rev-parse --git-path modules` gives `<common>/modules` in the main worktree and `<common>/worktrees/<id>/modules` in a linked one (git 2.47; `modules` isn't in git's common-dir list). No sibling shares them.
- **So they travel the way plain mode does:** one `rsync -a --delete` of the agent's own modules dir. That carries every submodule at every depth exactly as it is: branches, local commits, index, stash, reflog, HEAD, remotes, shallow/sparse state and in-progress ops.
- **Rejected: running G1–G4 per submodule.**
  - It carries only HEAD and the index; local branches, stashes and reflog are lost.
  - Its CAS means nothing on the usual detached HEAD.
  - A submodule missing on the destination needs a bootstrap clone from a URL that may be relative, private or local.
- **Still git-aware:**
  - Superproject gitlinks: already carried by G1's tree and G4's read-tree.
  - The shared `submodule.<name>.{url,active}` config: set only if unset.
  - Two pointers that encode the worktree id or depth: `<path>/.git` and `core.worktree`. G4 rewrites both.
- **Detection keys on gitlinks (mode 160000), not `.gitmodules`.** leoterm has an empty `.gitmodules` and no gitlinks, so it is not a submodule case.
- **Prototype:** verified locally on 2026-10-27 (see the header).

### §2, extends manifest and preflight
- **Manifest `git`** adds:
  - `modules_dir`: the source path, absolute.
  - `submodules`: `[{path, name, state:"absorbed"|"inline"|"uninit", gitdir, head_sha, head_ref, url, active}]`, recursive. `path` is relative to `ws`; `gitdir` is relative to `modules_dir`.
- **G3 response** adds `modules_dir` (the destination path, absolute) and `notes:[]`.
- **New code** `submodule_torn`. Submodule refusals reuse `git_unsupported`, `unsafe_path` and `branch_exists`.
- **Notes** are non-fatal and printed by the job: `submodule_url_differs`, `submodule_url_local`.

### §3, Git mode: replaces the refusal bullet "Shallow, sparse, or `.gitmodules` present" with "Shallow or sparse superproject", and adds:

**Submodule scan.** `git.Submodules(dir)` is recursive. The source runs it at P0; the destination runs it again in G4. All calls are exec with `-C` and no shell.
- **Gitlinks:** `ls-files --stage -z` entries with mode `160000`.
- **Names:** `config --file <dir>/.gitmodules -z --get-regexp '^submodule\..*\.path$'`. A gitlink with no entry is an embedded repo.
- **State, from `<dir>/<path>/.git`:**
  - missing → `uninit`;
  - a directory → `inline` (not absorbed, or embedded). It travels inside the workspace rsync and is not recursed into.
  - a file → `absorbed`. Its gitdir is `-C <dir>/<path> rev-parse --absolute-git-dir`; recurse into it.
- **Recorded per submodule:** `rev-parse HEAD`, `symbolic-ref -q HEAD`, and `-C <ws> config --get submodule.<name>.{url,active}`.

**Refused** (`git_unsupported` unless stated). Nothing has been stopped when these fire.
- Linked mode where `--git-path modules` resolves to `<common>/modules`, meaning that git version shares submodule dirs across worktrees. Checked on the source at P0 and on the destination at G3.
- An absorbed gitdir outside the agent's `modules_dir`.
- A submodule gitdir with entries in `worktrees/`, or with an `objects/info/alternates` file.
- A `path` or `name` that fails `^[A-Za-z0-9._@%+=,-][A-Za-z0-9._@%+=,/-]*$` or has a `..` component → `unsafe_path`.
- **Not refused:** a submodule that is shallow, sparse, or has an op in progress. Its private gitdir carries that state.

**Sequence additions**

| Stage | Side | Commands |
|---|---|---|
| G3 | dest | After the add or switch: `-C <ws> rev-parse --path-format=absolute --git-path modules`. It must equal `<absolute-git-dir>/modules`; in linked mode that git dir must be `<common>/worktrees/<id>`. |
| S5 | source | Adds `--exclude=/<path>/.git` for each `absorbed` submodule, nested ones included. `inline` and `uninit` paths are not excluded. |
| S5m (new, after S5) | source | If the source `modules_dir` exists: `rsync -a --delete --itemize-changes <src modules>/ <host>:<T>/`. T is the destination `modules_dir` for a move, or `<gitdir>/leo-modules.partial` for a copy. |
| G4 | dest | Under the lock, in order: (1) copy only: `worktree move`, then `os.Rename(<gitdir>/leo-modules.partial, <gitdir>/modules)`. (2) Superproject `read-tree`. (3) For each `absorbed` submodule, deepest first: write `<ws>/<path>/.git` = `gitdir: <filepath.Rel>` (Go, temp file + rename); `config --file <gitdir>/config core.worktree <rel>`, never `--git-dir`, because git chdirs to the stale worktree and dies; `-C <ws>/<path> update-index -q --refresh`; `rev-parse --verify -q HEAD^{commit}`. A copy also runs `fsck --connectivity-only --no-dangling --no-progress`. Either failure → `submodule_torn`. (4) Shared config, below. (5) Superproject refresh, unchanged. |

**Rules (submodules):**
- **Destination setup:** a submodule not initialised on the destination arrives with its gitdir, and G4 initialises it. No clone, no network, no `gh`.
  - A submodule uninitialised on the source gets no exclude, so S5's `--delete` removes a stale destination `<path>/.git`, and S5m's `--delete` removes its gitdir.
- **Shared config:** for each `url` or `active` value the source has, run `config submodule.<name>.<k> <v>` only when `config --get` is empty. Never run `submodule init`, `sync` or `update`, which would re-resolve URLs or fetch.
- **URLs and remotes:**
  - Each submodule's remotes live in its private config and arrive verbatim.
  - A relative `.gitmodules` URL is never re-resolved on the destination, even when the destination canonical has no origin; the source's resolved value is copied.
  - If the destination already has a different value, it is kept (sibling worktrees use it) and the job notes `submodule_url_differs`.
  - A URL that is a local path missing on the destination → `submodule_url_local`. The move doesn't need it; later fetches on that host will fail.
- **Copy staging:** `git worktree move` dies ("working trees containing submodules cannot be moved or removed") once `<gitdir>/modules` exists or a gitlink is populated. The staging name plus the excluded `.git` files keep the partial worktree movable. The admin id survives the move.
- **Copy branches** ⚑:
  - For each absorbed submodule with `head_ref=refs/heads/<b>`, in the copy's gitdir: `update-ref refs/heads/<b>-<new> <head_sha> <0×40>`, then `symbolic-ref HEAD refs/heads/<b>-<new>`.
  - A detached submodule stays detached.
  - `branch_exists` fires at P0 if `<b>-<new>` already exists in the source submodule.
  - `--branch` applies to the superproject only. No upstream is set, and the gitlinks don't change.
- **Agents sharing a canonical:** S5m writes only the agent's own `modules_dir`. G4's config writes are set-if-unset and run under `canonicalLock`.
- **Quoting (decision 6):**
  - The `path`/`name` regex above makes every `--exclude=` rule free of wildcards.
  - Both `modules_dir` values must pass `ValidatePath`.
  - `<modules_dir>/<gitdir>` must stay under `modules_dir` after `filepath.Clean`.
  - `.git` contents are written from Go and never reach a shell.

### §6, additions (move)
| # | Stage | On failure |
|---|---|---|
| P0 | Submodule scan and its refusals | Nothing touched. |
| G3 | Destination modules dir is shared or unexpected | `git_unsupported`. The worktree stays `ours` for the re-run; the source is stopped and unmarked. |
| S5m | Modules rsync | Same as S5. A partial dir sits only in this agent's private gitdir with no live record; a re-run converges. |
| G4 | Pointers, config, refresh, verify | Same as G4: a shadow record, and the source stays unmarked. Each step is idempotent, so a re-run rewrites it. |

**Copy:** the partial state is `<ws>-<new>.leo-partial` plus `leo-modules.partial`, and the next run reuses both. Rollback never deletes a modules dir and never runs `submodule deinit`, `worktree remove` or `prune`.

### §7, changed and new steps
Global rule added: `testenv.Git(t)` also sets `GIT_CONFIG_COUNT=1`, `GIT_CONFIG_KEY_0=protocol.file.allow`, `GIT_CONFIG_VALUE_0=always`. Fixtures use `submodule add ../x.git` against temp bare repos.

6. **Transport (changed).**
   - RED:
     - `TestWorkspaceRsyncExcludesAbsorbedSubmoduleGitFiles`: the nested absorbed path is excluded; inline and uninit paths are not.
     - `TestModulesRsyncArgvMoveAndCopyTarget`: exact argv for each mode.
   - Verify: `go test -race -run 'Rsync|ModulesRsync' ./internal/agentmove/`

6a. **Git primitives (changed).** New file `internal/git/submodule.go`.
   - Funcs: `Submodules`, `ModulesDir`, `RelinkSubmodule`, `ForkSubmoduleBranch`, `SetConfigIfUnset`, `VerifyHead`.
   - RED:
     - `TestSubmodulesScanStates`: absorbed nested, inline, uninit, embedded.
     - `TestSubmodulesEmptyGitmodulesNoGitlinks`: the leoterm shape returns none.
     - `TestModulesDirPerWorktree`.
     - `TestRelinkSubmoduleAcrossDepth`: main→linked; status shows `S...` until relinked.
     - `TestSetConfigIfUnsetKeepsExisting`.
     - `TestForkSubmoduleBranchCreateOnly`.
   - Verify: `go test -race -run 'Submodule|ModulesDir|Relink|IfUnset' ./internal/git/`

6b. **Git mode detection (changed).** `TestGitModeRefusesSubmodulesShallowSparse` becomes `TestGitModeRefusesShallowSparse`.
   - RED:
     - `TestGitModeEmptyGitmodulesNotRefused`: fails today on the `.gitmodules` rule.
     - `TestGitModeRefusesSharedModulesDir`.
     - `TestGitModeRefusesSubmoduleWorktreesAndAlternates`.
     - `TestSubmodulePathNameValidation`: a table of space, `..`, leading `-`, `*`, `[`, `$` and newline.
   - Verify: `go test -race -run 'GitMode|SubmodulePath' ./internal/agentmove/`

8. **Destination side (changed).**
   - RED:
     - `TestReceiveSubmodulesRoundTripPreservesStatus`: the prototype as a test. Nested submodules, dirty state, a stash, a local submodule branch, and a destination worktree id that differs. Asserts an equal hash over the superproject's `status --porcelain=v2 --branch` and `diff --cached`, plus each submodule's status, `diff --cached`, `stash list` and `for-each-ref`, recursively.
     - `TestReceiveSubmoduleURLNotReResolved`: a relative URL, and a destination canonical with no origin.
     - `TestReceiveSubmoduleConfigKeepsDestValue`: the note is emitted.
     - `TestGitPrepareRefusesSharedModulesDir`: faked `--git-path` output.
     - `TestConcurrentReceivesShareCanonical`, extended: one agent has submodules, and the sibling's modules-tree hash is unchanged.
   - Verify: `go test -race -run 'Receive|GitPrepare|Concurrent' ./internal/agent/ ./internal/agentmove/`

9. **Source orchestrator (changed).** Fault injection is extended to S5m and to the G4 submodule steps. It asserts the source is stopped and unmarked, the lock is released, and no modules dir was deleted.
   - Verify: `go test -race -run 'Move' ./internal/agentmove/ ./internal/daemon/`

10. **Copy (changed).**
   - RED:
     - `TestCopyWorktreeWithSubmodulesMovesPartial`: the naive order (rsync to `modules/`, then move) hits git's refusal.
     - `TestCopyForksAttachedSubmoduleBranches`.
     - `TestLocalCopyWorktreeSourceUntouched`, extended: the source modules-tree hash is unchanged.
   - Verify: `go test -race -run Copy ./internal/agentmove/ ./internal/agent/`

12. **E2E (changed).** Add `AgentMoveWorktreeSubmodule` (control plane). `AgentCopyWorktreeLocal` gains a nested submodule. The gate is unchanged.

### §8, live checks: replaces check 14's `.gitmodules` bullet, adds:
15. Move `leoterm-design` to the laptop and back. Expect no refusal and matching hashes. This is the real case: an empty `.gitmodules`.
16. The `move-test` repo gets a submodule that has its own nested submodule. In `move-wt`, the submodule gets:
    - an unpushed commit on a branch;
    - a stash;
    - a staged and an unstaged edit;
    - a dirty nested submodule.
    Then `move --to laptop`. The hashes match, including the recursive per-submodule hashes and `submodule status --recursive`. Moving back only sends what changed (`--itemize-changes`).
17. `copy --to laptop`: the submodule is on `<b>-<new>` and the superproject gitlinks are unchanged.

### §9, additions
**Deferred:**
- Remove "Submodules (`leoterm`)".
- Add: submodules that have their own worktrees or alternates.
- Add: submodule paths or names outside the safe charset.
- Add ⚑: foreign linked worktrees nested in a workspace, such as `leoterm/.claude/worktrees/agent-*`. Proposed fix: in git mode, add an S5 exclude for every `worktree list` entry under `ws`.

**Accepted risks:**
- A `git gc` inside a submodule re-sends its packs, because rsync works file by file.
- Submodule stashes and reflogs are carried, but the superproject's are not.
- A copy of a live source can hit `submodule_torn` and needs a re-run.
- A differing destination `submodule.<name>.url` is kept.
- `inline` gitdirs are rsynced whole.
