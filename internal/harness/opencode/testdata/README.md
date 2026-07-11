# Opencode stream fixtures

Captured 2026-07-10 from `opencode run --format json` (opencode 1.17.7).

- `fresh.jsonl` — successful run: `step_start` → `text` → `step_finish`; every event carries `sessionID`.
- `resume.jsonl` — `opencode run --format json -s <id> <prompt>`; same session id on every event.
- `badmodel.jsonl` — `--model anthropic/not-a-real-model`: a multi-line NON-JSON log blob interleaved on stdout, followed by two JSON `error` events (exit 1). Parsers must skip unparseable lines.
- `multistep_deny.jsonl` — two-step turn (denied `tool_use`, then final `text` "BLOCKED") captured with `OPENCODE_CONFIG_CONTENT='{"permission":{"bash":"deny"}}'`.
- `truncated_no_step_finish.jsonl` — `fresh.jsonl` with the final `step_finish` removed: the upstream #26855 shape (fixed in v1.16+ for local runs but still possible on older versions and `--attach`). EOF must be treated as end-of-turn.

A stale `-s` session id produces **no stdout** (`Error: Session not found` on stderr, exit 1) — no fixture needed; see the runner's stale-session text patterns.
