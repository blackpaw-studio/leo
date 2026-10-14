# Runs

## Run 2026-10-14 (started 2026-10-14T01:05:35Z)

🛠 leo autopilot — 2 fixed, 4 shipped, 1 blocked
Early stop: review.concurrency (codex-reviewer) failed twice — "interactive composer not ready"
Fixed
• B-021 Nested dispatch notifications never woke the parent — caller harness now resolved from the parent dispatch (not visually verified) · light
• B-002 "composer busy" on idle dispatches — codex truncates its placeholder in narrow panes; rows measured in cells (not visually verified) · full
Shipped
• B-001 Persistent tasks survive restart — already worked; e2e guard + mutation proof (not visually verified) · full
• B-004 Headless continuations delivered only after Start (not visually verified) · light→full
• B-003 Persistent codex/opencode no longer recorded as failures — turn hooks + opencode leo plugin (not visually verified) · full
• B-006 done, no code change — fixed in v0.35.0 (#223); issue #175 still open
• B-021, B-003, B-004 built before B-003/B-005 order (independent)
Calls I made — reply "veto D-0xx"
• D-005 B-001 no production change · D-013 go-runewidth direct dep · D-018 accepted cancel-race window · D-019 hooks as completion signal · D-021 opencode plugin in ~/Library/Caches/leo/opencode/ (D-005–D-024 in DECISIONS)
Needs you
• ⚠ Never-list breach: a B-003 live check used a bad LM Studio model id; opencode fell back to paid OpenAI for one tiny turn
• B-005 shelved on autopilot-shelved/B-005 — built + live-verified, concurrency review never ran. I'd pick: retry next run once codex-reviewer works
• Run guard false positive: `git -C $VAR merge/commit` denied as default-branch; literal paths pass
Ideas parked: B-022 Linux KillMode bounce · B-023 stale processes: docs · B-027 cold opencode timeout · B-028 no chat card on persistent fail · B-029 codex hooks dialog
Self-filed: B-024 bug false "dispatch failed" on rejected send · B-025 bug flaky TestBridgeReportAcceptsLargeTurnText · B-026 bug ~75s injection wait · dropped (over cap) leo run history rename race
Next up: B-007, B-008 (+B-024–B-026 next run)
Spend (estimated, lower bound · prices 2026-10-09): ≈$39.36 est. — B-001 $4.00 · B-021 $1.46 · B-002 $8.75 · B-004 $3.63 · B-003 $13.53 · B-005 $7.13 · B-006 $0.87
Interim updates: 1 posted, through item 5
Worktree: /Users/evan/.leo/agents/leo/.git/autopilot/worktree
Review: /autopilot-review leo
Merge when happy (from your default branch): git -C /Users/evan/.leo/agents/leo merge autopilot
