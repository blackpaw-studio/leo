# Opencode stream fixtures

Captured 2026-07-10 from `opencode run --format json` (opencode 1.17.7).

- `fresh.jsonl` — successful run: `step_start` → `text` → `step_finish`; every event carries `sessionID`.
- `resume.jsonl` — `opencode run --format json -s <id> <prompt>`; same session id on every event.
- `badmodel.jsonl` — `--model anthropic/not-a-real-model`: a multi-line NON-JSON log blob interleaved on stdout, followed by two JSON `error` events (exit 1). Parsers must skip unparseable lines.
- `multistep_deny.jsonl` — two-step turn (denied `tool_use`, then final `text` "BLOCKED") captured with `OPENCODE_CONFIG_CONTENT='{"permission":{"bash":"deny"}}'`.
- `truncated_no_step_finish.jsonl` — `fresh.jsonl` with the final `step_finish` removed: the upstream #26855 shape (fixed in v1.16+ for local runs but still possible on older versions and `--attach`). EOF must be treated as end-of-turn.

A stale `-s` session id produces **no stdout** (`Error: Session not found` on stderr, exit 1) — no fixture needed; see the runner's stale-session text patterns.

## Driver fixtures (Plan 4)

Captured 2026-07-11 from opencode 1.17.7 running `opencode serve` + `opencode run --attach` against it:

- `attach_fresh.jsonl` — the complete stdout of an attached run whose turn **completed server-side** (assistant replied, `finish: stop`): attach-mode event forwarding is lossy, so only a single `step_start` arrived — no `text`, no `step_finish`. The one event carries top-level `sessionID`. Process exit, not events, is the turn-end signal in attach mode.
- `session_list.json` — `opencode session list --format json -n 1` output (the `directory` value sanitized to `/tmp/leo-e2e-ws`). The list spans ALL projects; consumers filter by `directory` and take the newest `created`.
