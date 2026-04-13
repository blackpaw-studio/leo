---
description: Pick the top Todo card from the Leo Kanban board and advance it one step (ask questions → draft plan → implement → PR).
---

# /work-next — one iteration of the Leo kanban dev loop

You are the **builder** agent. A separate reviewer agent runs on PRs — your job ends when you push a PR.

This command does **one iteration** against the Leo Kanban board (https://github.com/orgs/blackpaw-studio/projects/3). A second agent runs `/loop Nm /work-next` to repeat. Do **not** loop inside this command.

## Prerequisites (verify silently, abort with a clear message if missing)

- `gh` CLI authenticated with `project` + `repo` scopes
- `git` configured with push access to `blackpaw-studio/leo`
- Working directory is a checkout of `blackpaw-studio/leo` on a clean `main`

Run these checks first. If anything fails, print the problem and stop.

```bash
gh auth status
git rev-parse --show-toplevel
git status --porcelain
```

## Constants

- **Project number**: `3`
- **Project owner**: `blackpaw-studio` (org)
- **Repo**: `blackpaw-studio/leo`
- **Worktree root**: `~/.leo/worktrees/leo`

## Fetch the board

Use `gh project item-list 3 --owner blackpaw-studio --format json --limit 50` to pull all items. Filter client-side for `status == "Todo"` (single-select field `Status`). Sort by issue number ascending — lowest number is top of queue.

If no Todo items: print `no cards in Todo — nothing to do` and exit 0.

## Pick one card

Take the first (lowest-numbered) Todo item. Call it `$ISSUE`. Capture:

- `issue_number`
- `title`
- `body`
- existing labels
- all comments (via `gh issue view $ISSUE --repo blackpaw-studio/leo --json body,comments,labels`)
- the project item id (for status updates)

## State machine — decide what this iteration does

Evaluate in order. Each branch ends the iteration (no fall-through).

### 1. Card has label `agent-skip`

Skip it. Move on to the next Todo card. If none, exit.

### 2. Card has label `needs-info` and no new user comments since the last agent `[needs-info]` comment

Still waiting on the user. Skip. Take next Todo card.

### 3. Card has label `needs-info` and the user has replied since your last `[needs-info]` comment

User provided clarifications. Proceed to step 5 (plan drafting).

### 4. Card has no plan label yet (neither `plan-drafted` nor `plan-approved`)

Evaluate whether the card has enough detail to plan. A card is **well-scoped** if all of these hold:

- A clear problem statement or user value is articulated
- At least one acceptance criterion, or the desired behavior is unambiguous
- Scope is bounded to roughly one PR's worth of work
- No contradictory requirements

**If not well-scoped**: post a comment with the `[needs-info]` prefix containing 2–5 specific clarifying questions. Add the `needs-info` label. Move the card to the `Needs Info` column. Exit.

Example questions comment:

```
[needs-info] A few questions before I can draft a plan:

1. Should this work for both `leo task add` and direct `leo.yaml` edits, or just one path?
2. What's the expected behavior when …?
3. Is there a migration concern for existing users on older configs?

Once answered, move this card back to Todo (or remove the `needs-info` label) and I'll draft a plan.
```

**If well-scoped** (or arriving here from step 3 after user clarified): go to step 5.

### 5. Draft a plan

Post a comment with the `[plan]` prefix. Structure:

```
[plan] Proposed approach for #<issue_number>

## Summary
<1-2 sentence recap of what we're building>

## Approach
<ordered list of concrete steps — files to touch, new files to add, refactors>

## Tests
<how this is verified — unit, integration, manual>

## Risks / open questions
<anything the reviewer or Evan should weigh in on>

## Out of scope
<explicit deferred items>

Reply with changes or add the `plan-approved` label to proceed.
```

Add label `plan-drafted`. Leave the card in Todo (so if the plan is approved it's picked up next iteration). Remove `needs-info` if present. Exit.

### 6. Card has `plan-approved`

Implement it. This is the main build path.

#### 6a. Move card to In Progress

Set the Status field on the project item to `In Progress`.

#### 6b. Create worktree + branch

```bash
mkdir -p ~/.leo/worktrees/leo
# slugify the issue title: lowercase, alphanumeric + dash
SLUG=$(echo "$TITLE" | tr '[:upper:]' '[:lower:]' | sed -e 's/[^a-z0-9]\+/-/g' -e 's/^-//;s/-$//' | cut -c1-40)
BRANCH="work/${ISSUE}-${SLUG}"
WORKTREE="~/.leo/worktrees/leo/${ISSUE}"

git fetch origin main
git worktree add -b "$BRANCH" "$WORKTREE" origin/main
```

#### 6c. Dispatch the coding subagent

Use the `Agent` tool (subagent_type: `general-purpose` unless a more specific reviewer fits the task). Prompt should include:

- The issue title and body
- The `[plan]` comment (most recent, which is the approved plan)
- Absolute path to the worktree
- Explicit constraints: work only inside `$WORKTREE`, run `make test` and `make lint` before declaring done, make small focused commits, do **not** push or open a PR — return to this command when done

Required return from the subagent: summary of changes, list of commits made, test/lint output.

#### 6d. Verify + push

Back in this command:

```bash
cd "$WORKTREE"
git log --oneline origin/main..HEAD   # sanity check commits exist
make test
make lint
```

If tests or lint fail: post a `[build-failed]` comment on the issue with the output, move card back to Todo, leave the branch on disk, exit. Do **not** open a PR.

If green:

```bash
git push -u origin "$BRANCH"
```

#### 6e. Open PR

```bash
gh pr create \
  --repo blackpaw-studio/leo \
  --base main \
  --head "$BRANCH" \
  --title "<type>: <concise description> (#$ISSUE)" \
  --body "<see template below>"
```

PR body template:

```
Closes #<issue_number>

## Summary
<what and why, from the approved plan>

## Changes
<bulleted list of concrete changes>

## Tests
<test output, manual verification steps>

## Plan reference
<link back to the approved [plan] comment on the issue>

---
🤖 Generated by /work-next — review by the reviewer agent.
```

Add the project item to the PR's linked items (GitHub does this automatically when the PR body has `Closes #N`).

#### 6f. Move card to In Review

Set Status = `In Review`. Remove the `plan-approved` label (so the card can't be re-picked). Exit.

### 7. Card already has `plan-drafted` but no `plan-approved`

Plan is awaiting human review. Skip. Take next Todo card.

## Never do

- Run `/loop` from inside this command.
- Push commits directly to `main`.
- Merge PRs. The reviewer agent handles merges.
- Pick up a card with label `agent-skip`.
- Work outside the assigned worktree.
- Proceed without an approved plan.
- `rm -rf` worktrees. Leave them for post-mortem; they GC when `git worktree prune` runs after the branch is deleted.

## On failure

If any step fails (network, auth, conflict, subagent returns empty), post a `[error]` comment on the card with the error, leave the card's column unchanged, and exit with a non-zero status so the outer `/loop` notices.

## Final message

After exiting, print one of:

- `nothing to do`
- `asked questions on #<N>`
- `drafted plan on #<N>`
- `opened PR <url> for #<N>`
- `build failed on #<N> — see comment`
- `error: <message>`

Keep it one line. The loop wrapper pipes this to its log.
