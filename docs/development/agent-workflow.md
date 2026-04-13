# Agent-Driven Development Workflow

Leo's day-to-day development runs through a kanban board + two Claude Code agents on `/loop`.
This doc explains the setup so a contributor (human or AI) can drop a card on the board and watch it ship.

## The board

**Leo Kanban** — https://github.com/orgs/blackpaw-studio/projects/3

Linked to `blackpaw-studio/leo`. All project cards are GitHub issues in this repo.

### Columns (Status field)

| Column | Meaning |
|--------|---------|
| **Todo** | Ready for the builder agent to pick up. |
| **Needs Info** | Builder posted clarifying questions; waiting on a human. |
| **In Progress** | Builder is implementing. |
| **In Review** | PR is open; the reviewer agent owns it. |
| **Done** | Merged. |

### Labels

| Label | Meaning |
|-------|---------|
| `needs-info` | Open clarifying questions on the card. |
| `plan-drafted` | Builder has posted a `[plan]` comment. Review it. |
| `plan-approved` | You approved the plan. Builder will implement on the next iteration. |
| `blocked` | External dependency. Builder will skip. |
| `agent-skip` | Never auto-pick. Use for exploratory or human-only cards. |

## The two agents

Both are Claude Code sessions running `/loop`. They're independent — run them in separate terminals / panes / Leo processes.

### 1. Builder (`/loop 10m /work-next`)

Command: [`.claude/commands/work-next.md`](https://github.com/blackpaw-studio/leo/blob/main/.claude/commands/work-next.md)

Does one iteration per tick:

1. Pulls Todo cards from the board.
2. Picks the lowest-numbered one.
3. If the card is under-scoped → asks questions in a comment, labels `needs-info`, moves to **Needs Info**, stops.
4. If the card has no plan → drafts a `[plan]` comment, labels `plan-drafted`, stops (awaits human approval).
5. If the card has `plan-approved` → creates a worktree, spawns a coding subagent, runs tests, opens a PR, moves to **In Review**.

### 2. Reviewer (`/loop 5m /review-next`)

Command: [`.claude/commands/review-next.md`](https://github.com/blackpaw-studio/leo/blob/main/.claude/commands/review-next.md)

Does one iteration per tick against open PRs on cards in **In Review**:

1. Pulls In Review cards → follows each to its PR, sorts oldest-updated first.
2. Skips drafts, `agent-skip`/`do-not-merge` PRs, and PRs where the HEAD sha hasn't changed since the last review.
3. Merge conflicts → attempts a clean `origin/main` merge, pushes if trivial; otherwise labels `blocked` and comments for human help.
4. CI still running → skips.
5. CI failed → `[ci-failed]` comment; if the failure pattern looks transient, kicks a re-run.
6. CI green → reads the diff, the linked issue, and the approved plan. Approves or requests changes.
7. Approved + green → squash-merges, deletes the branch, closes the issue, moves the card to **Done**, prunes the builder's worktree.

State is tracked at `~/.leo/state/review-next-cursor.json` (last reviewed HEAD sha per PR) so repeated iterations don't re-review unchanged PRs.

## Human responsibilities

- Write issues with enough detail to be actionable (problem, desired behavior, acceptance criteria).
- Answer `[needs-info]` questions in the card comments; remove the label or drag the card back to **Todo** when done.
- Review `[plan]` comments. If the approach is right, add the `plan-approved` label. Otherwise, reply with changes — the builder will redraft.
- Keep one PR's worth of scope per card. If a card balloons, split it.

## Worktrees

Builder creates worktrees under `~/.leo/worktrees/leo/<issue-number>/` off of `origin/main`. Branches are named `work/<issue>-<slug>`.

Worktrees stay until the branch is deleted (typically at merge). Run `git worktree prune` periodically to garbage-collect stale entries.

## Anti-patterns the builder must avoid

- Picking up cards with `agent-skip`.
- Proceeding without `plan-approved`.
- Pushing to `main`.
- Merging its own PRs.
- Running nested `/loop` commands.

## Running the board from scratch

1. Open the Kanban board and drop an issue in **Todo**.
2. Start the builder loop in a Claude Code session inside a `blackpaw-studio/leo` checkout:
   ```
   /loop 10m /work-next
   ```
3. (Optional) Start the reviewer loop in a second session.
4. Watch cards move right. Intervene when a card lands in **Needs Info** or has `plan-drafted`.
