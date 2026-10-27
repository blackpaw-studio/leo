# Amendment: worktree agents in v1 (git mode)

Amends `2026-10-27-leo-agent-move.md`. Each block names the plan section it replaces or extends; where they conflict, this file wins over the base plan, and the spec wins over both.

### §0, replaces decision 1, and marks decisions 2 and 3 settled
1. ✔ **Worktree agents move in v1 using git mode** (Evan, 2026-10-27).
   - **When git mode applies:** the workspace's `.git` is a file (a linked worktree), or its repo has linked worktrees on either host. The second case covers the 8 plain agents whose workspace is a shared canonical.
   - **What git mode never does:** rsync a git dir.
   - **How each part travels:**
     - Refs and commits: `git push` into `refs/leo-move/<agent>/*`.
     - The index: a tree object.
     - Working files: rsync without `/.git`.
     - The worktree itself: rebuilt with `git worktree add --no-checkout`.
   - **Plain mode:** repos with no linked worktrees on either host keep the spec's `.git` rsync.
   - **Prototype:** the sequence above was verified locally on 2026-10-27.
2. ✔ **Settled** (Evan, 2026-10-27): host identity is `os.Hostname()`, as above.
3. ✔ **Settled** (Evan, 2026-10-27): a persistent-task agent gets a warning, not a refusal.

### §1, addition: how worktrees are created and tracked today
- **Layout:**
  - `WorktreeRoot` is `<base>/.worktrees` (`internal/agent/workspace.go:36`).
  - `ResolveWorktreeLayout` gives `.worktrees/<repo-short>/<slug>` (`:180-208`).
  - `ResolveAgentWorktreeLayout` gives `.worktrees/<source-agent>/<slug>` (`:219-241`). Example: `leo-vitals-enhancements` lives under `.worktrees/leo-vitals/` while its canonical is `vitals`. So paths can't be derived and are carried as-is.
- **Canonical repo:**
  - `EnsureCanonical` runs `gh repo clone` (`workspace.go:130-151`, `:146`).
  - `AddWorktreeForBranch` picks local, tracking or new (`:251-274`).
  - git ops go through `git.ExecGit` (`internal/git/worktree.go:14`, `-C`, no shell). The add variants are at `:98-111`; `ErrBranchCheckedOut` at `:120`.
- **Spawn:**
  - `spawnFromAgent` uses `canonical = src.CanonicalPath || src.Workspace` (`internal/agent/manager.go:817-820`).
  - `spawnWorktreeCore` reserves the name, then fetch (`:919`), add (`:927`), rollback (`:938-947`), and Save with `Branch`/`CanonicalPath` (`:1000-1019`).
- **Tracking:**
  - `agentstore.Record.Branch`/`CanonicalPath` are at `internal/agentstore/store.go:42-43`.
  - `Delete` removes the worktree and optionally the branch (`manager.go:1856-1866`).
  - Restore treats `Branch != ""` as a worktree and **drops the record if the workspace is missing** (`internal/service/agents.go:110-125`). It then prunes canonicals (`:254`, `:370`).
  - Existing worktree tests disagree: `Branch` alone at `agents.go:110`, `Branch`+`CanonicalPath` at `manager.go:1799,1856`. Move uses `agentmove.GitMode()`, defined below.

### §2, addition: manifest and preflight
- **Portable-record "Sent" list** gains `branch` and `canonical_path`.
- **Manifest `git`:** `{mode:"linked"|"main", canonical, head_ref, head_sha, index_commit, root_shas, upstream:{remote,merge}, repo}`.
- **Preflight request:** the manifest's `git` block.
- **Preflight response `git`:** `{git_path, canonical_present, cloneable, has_root, branch_sha, checked_out:{<branch>:<path>}, linked_worktrees, ws_state:"absent"|"ours"|"foreign"}`.
- **New problem codes:** `canonical_missing`, `canonical_mismatch`, `branch_checked_out`, `branch_diverged`, `branch_exists`, `git_missing`, `git_unsupported`, `git_op_in_progress`, `unsafe_ref`. `workspace_exists` now means `ws_state=="foreign"`.

### §3, addition: Git mode
**Detection** (`agentmove.GitMode(ws)`, run on the source):
- `.git` is a file → `linked`. The canonical is the parent of `git rev-parse --path-format=absolute --git-common-dir`, and it must equal `rec.CanonicalPath` when that is set.
- A `.git` dir with `git worktree list --porcelain` > 1 entry, or the destination reporting `linked_worktrees>0` → `main`.
- Otherwise → plain.

**Refused** (`git_unsupported`, or `git_op_in_progress` for an in-progress git operation):
- A common dir that isn't `<x>/.git`.
- Shallow, sparse, or `.gitmodules` present.
- `MERGE_HEAD`, `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `BISECT_LOG`, `rebase-merge/` or `rebase-apply/` present (checked with `git rev-parse --git-path`).

**Sequence.** G1 to G3 run after S3 and before S5. G4 runs inside commit, before `post_receive`.

| Stage | Side | Commands (all exec, `-C <dir>`, no shell) |
|---|---|---|
| P3 seed (before stop) | dest | Only if the canonical is missing and `cloneable` (`rec.Repo` is owner/repo): `gh repo clone <repo> <canonical>.leo-partial`, then rename. A shared `CloneCanonical(repo, path)` is extracted from `EnsureCanonical`. |
| G1 snapshot | source | `write-tree` → `commit-tree <tree> -p HEAD -m leo-move` → `update-ref refs/leo-move/<agent>/index <c>`. Record `symbolic-ref -q HEAD` (empty means detached) and `rev-parse HEAD`. |
| G2 push | source | `git push --no-verify --porcelain --receive-pack="<git_path> receive-pack" <sshdest>:<canonical> +<head_sha>:refs/leo-move/<agent>/head +refs/leo-move/<agent>/index:refs/leo-move/<agent>/index`. Env: `GIT_SSH_COMMAND=<same ssh argv as rsync -e>`, `GIT_SSH_VARIANT=ssh`. The Local endpoint skips the push because it is the same repo. |
| G3 prepare | dest | Under a per-canonical lock: `merge-base --is-ancestor <branch_sha> refs/leo-move/<agent>/head` (fails → `branch_diverged`), then `update-ref refs/heads/<B> <new> <old-or-zero>`, a compare-and-swap. If `ws_state=absent`: `worktree add --no-checkout <ws> <B>` (or `--detach <ws> <sha>`). If `ours`: `symbolic-ref HEAD refs/heads/<B>` (detached: `update-ref --no-deref HEAD <sha>`). |
| S5 rsync | source | The workspace row adds `--exclude=/.git`. An excluded file is protected from `--delete`, so the destination's own `.git` pointer survives. |
| G4 finalize | dest | Under the lock: copy only `worktree move <ws>.leo-partial <ws>`; `read-tree 'refs/leo-move/<agent>/index^{tree}'`; `update-index -q --refresh`; set `branch.<B>.remote`/`merge` only if unset and the remote exists; `update-ref -d refs/leo-move/<agent>/{head,index}`. |
| G5 cleanup | source | After S7, best effort: `update-ref -d refs/leo-move/<agent>/index`. |

**Rules:**
- **Destination canonical:**
  - It must have one of the source's `root_shas`, else `canonical_mismatch`. This also covers repos with no origin that already exist on the destination.
  - Missing and not cloneable → `canonical_missing`, naming the clone command.
  - Missing commits travel through push negotiation, so nothing needs to be pre-fetched.
- **Branch held elsewhere:** `checked_out[B]` at a path other than `ws` → `branch_checked_out` with that path. The ref is never updated under a sibling worktree.
- **Early divergence (P1):** if the source lacks `branch_sha`, or that commit is not an ancestor of HEAD → `branch_diverged`. G3 re-checks with the actual objects.
- **`ws_state=ours`:** a linked worktree of the canonical whose destination record is absent, or a `moved_to` shadow with the same name. That is a move back or a re-run.
- **Source after a move:** keeps the worktree and the branch checkout, and the record gets `moved_to`. Restore must keep a moved worktree record even if its directory is missing (see §7.3).
- **Main mode:** same as linked, minus `worktree add`. The canonical *is* `ws`, and HEAD is switched by `symbolic-ref`. `workspace_in_use` already guarantees no live agent sits there.
- **Agents sharing one canonical:**
  - Per-agent ref namespace; push never writes `refs/heads/*` (so `denyCurrentBranch` never fires).
  - The destination daemon serialises G3/G4 per canonical (`Manager.canonicalLock(path)`).
  - Neither side touches another worktree's HEAD or index, and move code never runs `worktree prune`.
- **Copy:**
  - Namespace `refs/leo-move/<newname>/*`.
  - G3 runs `worktree add --no-checkout -b <cur>-<new> <ws>-<new>.leo-partial <head_sha>`.
  - `branch_exists` if that branch exists in either canonical (unless it is our partial).
  - G4 runs `worktree move` to `<ws>-<new>`.
  - The record gets `Branch=<new branch>` and the same `CanonicalPath`. A copy of a main-mode agent therefore becomes a linked-worktree agent.
  - Local copy: G1, G3 and G4 run against the same repo; the source worktree is only read.
- **Quoting (decision 6):**
  - git runs `GIT_SSH_COMMAND` through `sh -c`, so every token (host, `ssh_args`, ControlPath) must match `^[A-Za-z0-9._@%+=,:/-]+$`.
  - `canonical` and `git_path` must pass `ValidatePath`.
  - The agent name and branch must pass `ValidateRef`: git check-ref-format rules, no leading `-`, no `..`, no `.lock`. Otherwise `unsafe_ref`.
  - Refspecs never reach a shell.
- **Not carried:** stashes (repo-global, can't be tied to one agent), reflog, `ORIG_HEAD`, intent-to-add and skip-worktree bits, and LFS objects of unpushed commits. Gitignored files follow the spec (sent unless in `move.exclude`).

### §6, additions (move)
| # | Stage | On failure |
|---|---|---|
| P0 | Now also: git-mode detection and refusals, `ValidateRef`, ssh tokens. The "not a worktree" check is **removed**. | Nothing touched. |
| P1 | Git preflight fields and the new codes | Nothing touched. |
| P3 | Seed (clone) | Nothing stopped. A partial clone stays in `.leo-partial` and is replaced on the next run. |
| G1 | Snapshot (a git operation may have started after P0) | Source **stopped and unmarked**; the local temp ref is deleted; the "`leo agent start X` recovers it" text applies. |
| G2 | Push | Same as G1. The destination may hold `refs/leo-move/<agent>/*`, which is harmless and overwritten next run. |
| G3 | Prepare | `branch_diverged`: nothing on the destination changed (the check runs before the compare-and-swap). An add failure after the branch FF or create leaves the branch (a harmless FF) and an `ours` worktree for the re-run. Source stopped and unmarked. |
| G4 | Finalize | Treated like an S6 failure: the destination record becomes a `moved_to=<source>` shadow, the source stays unmarked, and the git error is shown. |
| G5 | Cleanup | Logged only. |

**Copy:** the source is never stopped. A failure leaves `<ws>-<new>.leo-partial` registered as a worktree, and the next run reuses it.

### §7, changed and new steps
Global rule added: git-mode tests run **real git** in `t.TempDir()`, using a `testenv.Git(t)` helper (`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, identity env). Only ssh/push argv are asserted through fakes. The local Bash hook blocks the literal `--no-verify`, so exercise push through Go only.

3. **Surfaces (changed).**
   - Add to `internal/service/agents.go:110-125`: check `MovedTo` before the missing-workspace drop.
   - RED: `TestRestoreKeepsMovedWorktreeRecordWithMissingWorkspace`.
6. **Transport (changed).**
   - Add `PushArgv(res, canonical, gitPath, refspecs)` and `GitSSHCommand(res)`.
   - RED:
     - `TestPushArgvExact`: argv, `--receive-pack` value, and `GIT_SSH_COMMAND`/`GIT_SSH_VARIANT` env.
     - `TestGitSSHCommandRejectsShellMeta`: `$`, backtick, `;`, quote, space, `|`, newline in host, `ssh_args` or ControlPath.
     - `TestValidateRef` (table).
   - Verify: `go test -race -run 'Push|GitSSH|ValidateRef' ./internal/agentmove/`
6a. **Git primitives (new).** File: `internal/git/move.go`.
   - Funcs: `SnapshotIndex`, `HeadState`, `IsAncestor`, `UpdateRefCAS`, `WorktreeAddNoCheckout`, `WorktreeMove`, `ReadTree`, `RefreshIndex`, `SetHEAD`, `WorktreeList`, `OpInProgress`, `RootCommits`, `DeleteRefs`.
   - RED (real git):
     - `TestSnapshotRoundTripPreservesStatus`: staged, unstaged, untracked and an unpushed commit give an identical `status --porcelain=v2 --branch`. This repeats the prototype.
     - `TestUpdateRefCASRefusesStaleOld`.
     - `TestOpInProgressDetectsRebaseAndMerge`.
     - `TestWorktreeListReportsCheckedOutBranches`.
   - Verify: `go test -race -run 'Snapshot|CAS|OpInProgress|WorktreeList' ./internal/git/`
6b. **Git mode detection and plan (new).** File: `internal/agentmove/gitmode.go`.
   - RED: `TestGitModeLinkedMainPlain`, `TestGitModeRefusesSubmodulesShallowSparse`, `TestGitModeCanonicalMustMatchRecord`, `TestWorkspaceRsyncExcludesDotGitInGitMode`.
   - Verify: `go test -race -run GitMode ./internal/agentmove/`
8. **Destination side (changed).**
   - `CheckDestination` adds the git codes and `ws_state`.
   - New verbs and routes: `move-receive git-seed|git-prepare` (`POST /agents/move/git-{seed,prepare}`).
   - `Receive` runs G4 before `post_receive`, under `canonicalLock`.
   - RED:
     - `TestCheckDestinationGitCodes`: a table over every new code plus `ours` vs `foreign`.
     - `TestGitPrepareCreatesWorktreeNoCheckout`.
     - `TestGitPrepareMoveBackFastForwardsOwnWorktree`.
     - `TestGitPrepareRefusesDivergedBranch`: the destination branch is unchanged.
     - `TestGitPrepareRefusesBranchCheckedOutElsewhere`.
     - `TestSeedClonesViaPartialRename`: `gh` argv faked, rename asserted.
     - `TestConcurrentReceivesShareCanonical`: two agents and two branches run G3 and G4 in goroutines; both statuses match and a sibling worktree's HEAD and index hash are unchanged.
     - `TestMainModeLeavesDestLinkedWorktrees`.
   - Verify: `go test -race -run 'Receive|CheckDestination|GitPrepare|Seed|Concurrent|MainMode' ./internal/agent/ ./internal/agentmove/ ./internal/daemon/`
9. **Source orchestrator (changed).**
   - P3 and G1 to G5 are inserted in order; `TestMoveRefusesWorktreeAgent` is **deleted**.
   - RED:
     - `TestMoveWorktreeHappyPathOrder`: verbs and argv in order.
     - Fault injection at P3 and G1 to G4, asserting the source is stopped and unmarked, the temp ref is gone, and the lock is released.
     - `TestMoveWorktreeEndToEndLocalPair`: two temp repos with a `file://` push through a Local-pair endpoint. The source keeps the worktree plus `moved_to`; the destination's status is identical.
   - Verify: `go test -race -run 'Move' ./internal/agentmove/ ./internal/daemon/`
10. **Copy (changed).**
    - RED: `TestCopyWorktreeNewBranchAndPath`, `TestCopyWorktreeBranchExistsRefused`, `TestLocalCopyWorktreeSourceUntouched` (source HEAD, index and status hash unchanged), `TestCopyMainModeBecomesLinkedWorktree`.
    - Verify: `go test -race -run Copy ./internal/agentmove/ ./internal/agent/`
12. **E2E (changed).** Add to `e2e/agent_move_test.go`:
    - `AgentMoveWorktree`: control plane on one host. The canonical is shared, so G2 to G4 really run but end in a no-op. Assert that the fakessh log shows `<git_path> receive-pack '<canonical>'` and no manifest bytes, that `moved_to` is set, and that a sibling worktree's status hash is unchanged.
    - `AgentCopyWorktreeLocal`: full run. Branch `<b>-<new>` exists, both agents run, and `git worktree list` shows both.
    - Gate: unchanged, plus `E2E_RUN='AgentMove|AgentCopy'`.

### §8, additions (live checks)
Setup: `gh auth status` succeeds on the laptop; `move-test` uses a small owner/repo of Evan's choosing, and the laptop has **no** clone of it yet.

8. On Dionysus:
   - Spawn `move-test --repo <o/r> --branch move-wt` plus a sibling `--branch move-sib`.
   - In `move-wt`, make an unpushed commit, a staged hunk, an unstaged edit, an untracked file and a gitignored file.
   - Save `status --porcelain=v2 --branch | shasum`, plus `rev-parse HEAD` and `diff --cached | shasum`.
9. `move --to laptop` → the clone happens, all three hashes match on the laptop, and the agent resumes. On Dionysus, `move-sib` hashes are unchanged and `worktree list` still shows `move-wt`.
10. On the laptop, commit and edit, then `move --to dionysus` → the branch fast-forwards on Dionysus and `--itemize-changes` lists only the edits.
11. Move to the laptop again → the push sends only the new objects (check the push stats).
12. `copy --to laptop` → branch `move-wt-<new>` and worktree `<ws>-<new>` exist, both agents run, and turns don't cross.
13. Move the plain agent whose workspace is the canonical → `worktree list` and the sibling hashes on the laptop are unchanged.
14. Refusals, each with nothing stopped:
    - `git worktree add` the branch at another path on the laptop → `branch_checked_out` naming that path.
    - A repo with `.gitmodules` → `git_unsupported`.

### §9, additions
**Deferred:** remove "Worktree agents" from the list. Add:
- Seeding a canonical with no origin (`plane-spotter`).
- Submodules (`leoterm`).
- Moving stashes and reflogs.
- Shallow and sparse repos.
- LFS objects of unpushed commits.
- Removing stray `ours` worktrees and partial worktrees left after an abandoned move.

**Accepted risks:**
- The per-canonical lock doesn't cover a concurrent `leo agent spawn --branch` on the same canonical. git's ref and worktree locks still apply; a `config.lock` collision is a retryable error.
- Intent-to-add and skip-worktree bits are lost.
- A diverged branch refuses the move and must be resolved by hand.
- The source keeps a stale worktree, holding the branch and the disk space, by design.
- Gitignored trees can be large on the first move.
- The `--receive-pack` path depends on the destination daemon's `PATH` (`git_path` comes from preflight).
