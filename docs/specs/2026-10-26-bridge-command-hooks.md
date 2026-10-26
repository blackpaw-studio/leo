# Bridge command hooks: survive cc-plugin-sec-default

Status: draft · 2026-10-26

## Problem

Claude Code ≥2.1.308 ships a built-in mod, `cc-plugin-sec-default`. When it is seated, it passes mod events on past person-installed mods. Our `leo-bridge` mod then never receives them, and CC posts a "leo-bridge: its <event> hook was skipped…" notice for every event (up to 16 per session).

The guard is seated when **managed `prependPlugins` lists it**. When that key is unset, it is seated if **managed settings exist**, **a managed MCP file exists**, or **the account's org type is team/enterprise**. Once seated, it stays seated for the session. Any external Leo user on a Team/Enterprise account hits this.

The guard passes over (from its debug-log registration and bundle constants):
- `classic.*`
- `prompt.section`, `prompt.context`, `prompt.attachment`, `prompt.compose`
- `skill.prompt`
- `attribution.text`
- `session.compact`
- `settings.read`
- the `session.append` doors `attachment`, `hook-context` and `compaction`

**Verified 2026-10-26 (CC 2.1.310):** with the guard seated by managed `prependPlugins`, plain `type: "command"` hooks from `--settings` and from a plugin's `hooks/hooks.json` all fired: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse and Stop. The guard affects only mod-API events.

**Root cause of #276/#277 "no classic.* under CLAUDE_CONFIG_DIR":** every Leo environment dir is logged into a `claude_team` account, and `~/.claude` is `claude_pro`. The guard seated on org type. Config dirs were never the cause.

## Goal

Every bridge feature that today depends on a passed-over event moves to Claude command hooks, for every Claude launch that loads the bridge (agents and dispatches). With the guard seated:
- the bridge produces zero "hook was skipped" notices;
- no Leo feature degrades.

## Design

Command hooks become the only path. We do not detect the guard and switch at runtime, and we do not run both paths.

1. **Remove passed-over subscriptions from the mod.** `leo-bridge` must not register for any event in the guard's passed-over set (list above). It keeps engine events (`turn.start`, `turn.complete`, `turn.step`, `tool.call`, …), `$.prompt.submit`, `$.store`, `$.model.fork`.
2. **Add a guard-set test.** A bridge test asserts that the mod's registered events ∩ the passed-over set = ∅. The set is a constant in the test and cites this spec.
3. **Move features to command hooks.** Extend the Go hook builders in `internal/harness/claude` (`hooks.go`, `settings.go`) so the agent and dispatch launch paths both carry:

   | Feature (was) | Command hook | Notes |
   |---|---|---|
   | Attention: permission (`classic.PermissionRequest`) | PermissionRequest | Exists for both paths; confirm the agent path raises attention |
   | Attention: elicitation (`classic.Elicitation`) | Elicitation, or Notification `elicitation_dialog` | Dispatch path has none today |
   | Attention clear (`classic.PostToolUse` / `PostToolUseFailure` / `PermissionDenied`) | Same events | — |
   | Subagent count (`classic.SubagentStart` / `SubagentStop`) | SubagentStart / SubagentStop | Report into the same state the bridge hello and `/api/v1` read today |
   | Stop pending: background_tasks, session_crons (`classic.Stop`) | Stop | Exists for dispatches (#277); ensure agents get it too |
   | Compaction (`session.compact`) | SessionStart matcher `compact`, or PreCompact | Whichever carries what the consumer needs |
   | Delegation roster (`prompt.compose`) | SessionStart + UserPromptSubmit `additionalContext` | See 4 |

4. **Delegation injection.**
   - A SessionStart hook covering `startup`, `resume`, `clear` and `compact` emits the delegation section as `additionalContext`.
   - A UserPromptSubmit hook emits it again only when the roster changed since the session was last told.
   - "Told" state moves from the mod's `$.store` to the daemon, keyed by agent and session id. The existing change detector (`helloIfSessionChanged`) reads it there.
   - The hook command is a `leo` subcommand that prints the hook JSON. The daemon owns the content.
5. **Diagnostics.** `leo doctor` adds one informational line on whether the guard will seat for each Claude config dir Leo uses. The line covers managed `prependPlugins`, the presence of managed settings, and the account's org type from `<config dir>/.claude.json`. It says nothing breaks either way. No roster or chat surface, since nothing degrades.
6. **Dedup.** Where a dispatch today gets the same event from both the bridge and a command hook, the bridge copy goes away with step 1. The command hook stays.

## Out of scope

- Oneshot tasks (`claude -p`, no bridge).
- Codex/opencode.
- Asking upstream for a mod trust opt-out.
- Server-managed `prependPlugins` latch bug (feedback already queued).

## Verification

- Unit tests per moved feature through the hook command's public interface: hook JSON in, then report or stdout out.
- Tests assert the argv and settings JSON the launch path generates, not mocked seams. See the memory note on mocked exec seams.
- **Live gate (required):** launch one agent and one interactive dispatch, both with the guard seated (temporary managed `prependPlugins: ["cc-plugin-sec-default@builtin"]`, then restored). Check:
  - zero "hook was skipped" notices;
  - permission attention raised and cleared;
  - subagent count moves;
  - background_tasks reaches `waiting`;
  - delegation section present at start and after `/clear` and `/compact`;
  - roster change mid-session is told once.
- Repeat unseated to confirm no duplicate injection.
