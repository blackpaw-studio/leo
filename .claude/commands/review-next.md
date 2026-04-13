---
description: Review the next open PR produced by /work-next. Check CI, code-review, request changes or auto-merge. One iteration; use with /loop.
---

# /review-next — one iteration of the Leo kanban review loop

You are the **reviewer** agent. A separate builder agent runs `/work-next`. Your job starts when a PR is open and a card sits in **In Review**.

Like `/work-next`, this is **one iteration**. A human wraps it in `/loop Nm /review-next`. Do **not** loop inside this command.

## Prerequisites

- `gh` CLI authenticated with `project` + `repo` scopes
- Write access to `blackpaw-studio/leo`
- Branch protection on `main` either off, or the token is in an allow list

Run and sanity-check:

```bash
gh auth status
gh repo view blackpaw-studio/leo --json defaultBranchRef
```

## Constants

- **Project number**: `3`
- **Project owner**: `blackpaw-studio` (org)
- **Repo**: `blackpaw-studio/leo`
- **Merge strategy**: squash
- **CI must be**: green (all required checks passed)
- **Review state file**: `~/.leo/state/review-next-cursor.json` — tracks the last reviewed HEAD sha per PR to avoid re-reviewing an unchanged PR

## Gather the queue

1. Pull project items where Status = `In Review`:
   ```bash
   gh project item-list 3 --owner blackpaw-studio --format json --limit 50
   ```
2. For each item, follow the linked PR. If the item has no open PR, log it and skip.
3. Sort PRs by `updated_at` ascending (oldest first — fairness).

If the queue is empty: print `no PRs to review` and exit 0.

## Pick one PR

Take the oldest. Call it `$PR`. Capture:

- PR number, head sha, base branch, head branch
- title, body
- linked issue number (from `Closes #N` in body or the card link)
- mergeable state, mergeStateStatus
- file list + diff (`gh pr diff $PR --repo blackpaw-studio/leo`)
- CI status: `gh pr checks $PR --repo blackpaw-studio/leo --json`
- existing reviews and review comments

## State machine — exactly one branch executes

### 1. PR is a draft

Skip. Move on to the next PR. If none left, exit.

### 2. PR has label `agent-skip` or `do-not-merge`

Skip.

### 3. PR is not mergeable (conflicts)

Attempt a merge of `origin/main` into the PR branch:

```bash
# in a scratch worktree
WORKTREE=~/.leo/worktrees/leo-review/$PR
git worktree add "$WORKTREE" origin/$HEAD_BRANCH
cd "$WORKTREE"
git fetch origin main
git merge --no-edit origin/main
```

- If the merge succeeds with no manual edits required → push the merge commit back up:
  ```bash
  git push origin HEAD:$HEAD_BRANCH
  ```
  Exit with message `resolved conflicts on #$PR`. Next iteration re-evaluates.

- If the merge needs resolution → abort (`git merge --abort`), post a `[conflict]` comment on the PR summarizing the conflicting files, apply label `blocked`, **do not** attempt AI merge-resolution blind. Exit with `conflicts on #$PR — human needed`.

Tear down the scratch worktree on exit: `git worktree remove "$WORKTREE"`.

### 4. CI is still running (any check pending/queued/in_progress)

Skip. Next iteration. Print `ci running on #$PR`.

### 5. CI has failed checks

Post a `[ci-failed]` comment on the PR summarizing which check failed and, if available, the first ~30 lines of the failing log. Do **not** approve. Leave the PR open, do not move the card.

If the failure looks transient (timeout, flaky test identified by keyword match), add the comment but also kick a re-run:
```bash
gh run rerun --failed --repo blackpaw-studio/leo <run-id>
```

Exit with `ci failed on #$PR`.

### 6. CI is green and you have already reviewed this HEAD sha

Check the cursor file `~/.leo/state/review-next-cursor.json`. If an entry exists for this PR with `last_reviewed_sha == current_head_sha` and `last_outcome == "approved"`:

- If the PR is still open: it's approved but merge hasn't landed. Attempt merge (step 8).
- Otherwise: skip.

If `last_outcome == "requested_changes"` and the HEAD sha is unchanged, skip — builder hasn't pushed a new commit yet.

### 7. CI is green, new HEAD sha since last review (or first time)

Do a real review. Read:

- the full PR diff
- the linked issue body and the most recent `[plan]` comment
- any prior review comments you or anyone else left

Evaluate against:

- **Correctness**: does the diff actually solve the card's acceptance criteria?
- **Scope**: does the diff match the approved plan? Scope-creep is grounds for changes.
- **Safety**: error handling, input validation, no secrets, no obvious injection risks.
- **Quality**: tests exist and cover the change, no dead code, files under 800 lines, functions under 50.
- **Style**: matches repo idioms (check `CLAUDE.md`, adjacent code).

Decide one of:

**(a) Approve** — all criteria met, no blocking issues.
```bash
gh pr review $PR --repo blackpaw-studio/leo --approve --body "<summary>"
```
Update cursor file: `{pr: {last_reviewed_sha: SHA, last_outcome: "approved"}}`. Proceed to step 8.

**(b) Request changes** — at least one blocking issue.
```bash
gh pr review $PR --repo blackpaw-studio/leo --request-changes --body "<summary + numbered list>"
```
For inline issues, use `gh api repos/blackpaw-studio/leo/pulls/$PR/comments` to leave line comments. Keep the review body concrete:

```
[review] Requesting changes — numbered items:

1. internal/foo.go:42 — missing error handling on fooBar()
2. internal/foo_test.go — no test exercises the new branch at line 90
3. Plan mentioned we'd also update docs/foo.md — missing

Fix and push; I'll re-review on the next iteration.
```

Update cursor file: `last_outcome: "requested_changes"`. Also: remove the `plan-approved` label from the linked issue so the builder understands it needs to act on review feedback (otherwise the next `/work-next` would pick a new card instead of fixing this one).

Exit with `requested changes on #$PR`.

**(c) Comment only** — non-blocking feedback, no decision. Use sparingly; prefer (a) or (b).

### 8. Approved + CI green — merge

```bash
gh pr merge $PR --repo blackpaw-studio/leo --squash --delete-branch
```

After merge:

- Update the card Status to `Done`
- Remove `plan-approved`, `plan-drafted` labels from the issue
- Close the issue if `Closes #N` didn't already auto-close it (`gh issue close`)
- Clear the cursor entry for this PR
- Garbage-collect the builder's worktree for the issue if present:
  ```bash
  [ -d ~/.leo/worktrees/leo/$ISSUE ] && git worktree remove --force ~/.leo/worktrees/leo/$ISSUE
  git worktree prune
  ```

Exit with `merged #$PR`.

## Cursor file format

`~/.leo/state/review-next-cursor.json`:

```json
{
  "23": { "last_reviewed_sha": "abc123", "last_outcome": "approved", "reviewed_at": "2026-04-13T19:40:00Z" },
  "24": { "last_reviewed_sha": "def456", "last_outcome": "requested_changes", "reviewed_at": "2026-04-13T19:42:00Z" }
}
```

Create the file if missing. Treat a missing entry as "never reviewed".

## Never do

- Approve without reading the diff.
- Merge without a green CI.
- Rebase-resolve conflicts by rewriting code you don't understand — post `[conflict]` and stop.
- Run nested `/loop`.
- Merge anything with `agent-skip` or `do-not-merge` labels.
- Touch `main` directly.
- Re-review an unchanged HEAD sha.

## On failure

Any failed `gh` call: post an `[error]` comment on the PR if possible, log, exit non-zero. Don't silently drop an iteration.

## Final message

One of:

- `no PRs to review`
- `ci running on #$PR`
- `ci failed on #$PR`
- `resolved conflicts on #$PR`
- `conflicts on #$PR — human needed`
- `approved #$PR`
- `requested changes on #$PR`
- `merged #$PR`
- `error: <message>`
