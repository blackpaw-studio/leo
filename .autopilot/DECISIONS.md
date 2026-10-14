# Decisions — Leo

To veto a decision, say "veto D-0xx" to any agent with the autopilot skill (it queues the reply through `autopilot-inbox`; the next run applies it), or tick the box in the worktree copy (`.git/autopilot/worktree/.autopilot/DECISIONS.md`) only. The copy on the default branch is read-only history.

## D-001 · 2026-10-13 · Maintain CHANGELOG.md
Context: CHANGELOG.md untouched since #246; Leo has external users now.
Chose: each item adds an `[Unreleased]` entry; B-015 backfills #247–#271.
Why: principle 4 (loud failures / honest reporting) and external users need to see breaking changes.
Alternatives: delete it (rejected by owner); leave it stale.
Commit:
Veto: n/a (owner)

## D-002 · 2026-10-13 · No dispatches page in the web UI
Context: follow-up listed in the subagent-dispatch and interactive-dispatch specs.
Chose: dropped; added to non-goals.
Why: leoterm and iOS cover dispatch viewing.
Alternatives: build after reliability; keep as idea.
Commit:
Veto: n/a (owner)

## D-003 · 2026-10-13 · Roster keeps active time, adds idle-since
Context: active time freezes while a dispatch is idle, which reads as a stall.
Chose: keep active time; append `idle <dur>` when idle (B-017).
Why: principle 4 — the frozen clock should read as intentional, not broken.
Alternatives: wall-clock elapsed; no change.
Commit:
Veto: n/a (owner)

## D-004 · 2026-10-13 · Milestone is reliability; hosted Leo left open
Context: vision session.
Chose: bugs and false outcomes first; cloud/hosted Leo is neither a goal nor a non-goal and sits on the stop list.
Why: owner call.
Alternatives: features first.
Commit:
Veto: n/a (owner)

## D-005 · 2026-10-13 · B-001: no production change — adoption already works
Chose: persistent-task targets are ordinary agents since #115 and RestoreAgents adopts surviving sessions; added an e2e regression guard with a mutation proof instead.
Why: BACKLOG "verify first"; principle 2.
Alternatives: rewrite the restore path.
Commit: 021fb4aee10452608528b48de966ffa0159b0509
Veto: [ ]

## D-006 · 2026-10-13 · B-001: config `processes:` criterion treated as met
Chose: the key was folded into agents in #112, so nothing remains to adopt.
Why: scope cut (AUTONOMY: small scope cuts).
Alternatives: reintroduce processes.
Commit: 021fb4aee10452608528b48de966ffa0159b0509
Veto: [ ]

## D-007 · 2026-10-13 · B-001: CHANGELOG entry for a test-only item
Chose: one-line [Unreleased] entry.
Why: D-001.
Alternatives: no entry.
Commit: 021fb4aee10452608528b48de966ffa0159b0509
Veto: [ ]

## D-008 · 2026-10-13 · B-001: restart checks use kill -9, not SIGTERM
Chose: SIGKILL is what kickstart -k does; SIGTERM shuts down gracefully and kills sessions by design.
Why: principle 2.
Alternatives: pkill (SIGTERM) as verify.md says.
Commit: 021fb4aee10452608528b48de966ffa0159b0509
Veto: [ ]

## D-009 · 2026-10-13 · B-001: e2e uses the fake codex harness
Chose: a claude-bridge relaunch could change pane_pid for reasons unrelated to adoption.
Why: principle 2.
Alternatives: fake claude bridge.
Commit: 021fb4aee10452608528b48de966ffa0159b0509
Veto: [ ]

## D-010 · 2026-10-13 · B-002: no tty-based pane lookup
Chose: pane id %N is stable across moves; rejection errors name the tty and location instead.
Why: principles 1, 4 (a tty fallback could paste into an unrelated pane).
Alternatives: tty → pane lookup on send.
Commit: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad
Veto: [ ]

## D-011 · 2026-10-13 · B-002: prose without a composer row reads as unknown, not busy
Chose: only a live status line or spinner means busy.
Why: principle 4.
Alternatives: scan whole transcript for busy words.
Commit: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad
Veto: [ ]

## D-012 · 2026-10-13 · B-002: dropped post-move redraw settle
Chose: never reproduced live; truncation fix covers stale narrow frames.
Why: principles 2, 4.
Alternatives: poll for empty composer after rejoin.
Commit: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad
Veto: [ ]

## D-013 · 2026-10-13 · B-002: go-runewidth promoted to a direct dependency
Chose: already indirect (MIT, go.sum unchanged); composer rows measured in terminal cells, ambiguous width = 1.
Why: principle 4.
Alternatives: count runes.
Commit: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad
Veto: [ ]

## D-014 · 2026-10-13 · B-002: accepted risk on placeholder-prefix drafts
Chose: a human draft identical to a truncated placeholder prefix at the pane edge reads as empty.
Why: principle 4.
Alternatives: stricter match that rejects idle narrow panes.
Commit: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad
Veto: [ ]
