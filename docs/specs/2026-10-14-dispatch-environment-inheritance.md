# Dispatch environment inheritance

Status: draft, awaiting Evan's approval (2026-10-14)

## Problem

`leo_dispatch` children resolve environments from their template only
(`cfg.TemplateEnv` → `EnvironmentNames(nil, tmpl.Environments)`), never from
the caller. An agent running under environment `work` therefore dispatches
children under the template/default environment.

Interactive dispatches also leak inconsistently: the pane splits inside the
caller's tmux session, whose session env (from `new-session -e`) shows
through for every key the child is not given. Headless children start from
the daemon's `os.Environ()` and get none of it. The same dispatch can end up
on different accounts depending on `mode`.

## Behaviour

1. **Inherit.** A dispatch child's environment list is the caller's
   *effective* environment list (after defaults, template,
   and spawn/`leo_set_agent_environments` overrides), layered with the
   child template's list.
2. **Precedence: caller over template.** Resolve the template's environments
   first and the caller's on top. When both set the same key, the caller
   wins. Within each list, the existing left-to-right merge still applies.
3. **Override.** `leo_dispatch` (MCP tool, HTTP API, and `leo dispatch` CLI)
   gains an optional `environments: [names]`. When set, it replaces the
   inherited (caller) layer, and the template layer still applies beneath
   it. `[]` means "inherit nothing" (template/defaults only). Unknown names
   are rejected at dispatch time with the existing `UnknownEnvironmentError`.
4. **Callers with no environments.** CLI or other non-agent callers have no
   caller layer, so today's behaviour is unchanged.
5. **Nested dispatch.** A dispatch run records its effective environment
   names. A child dispatched by that run inherits them (rule 1 applied
   recursively).
6. **Continuations.** `leo_send_dispatch` resumes reuse the run's recorded
   environments (`continuation.go` must not re-resolve from the template
   alone).
7. **Mode parity.** Headless and interactive children get an identical
   resolved env. For interactive panes, any key in the caller's tmux
   session env that the child's resolved env does not set is blanked via
   `-e KEY=`, so nothing shows through implicitly. Leo's reserved keys are
   already handled this way.

## Observability

The dispatch record (roster, `/api/v1`, `leo dispatch output`) shows the
child's effective environment names and whether each came from `inherited`,
`override`, or `template`. Names only, never values.

## Model and effort

Model and effort are already overridable per dispatch
(`resolved.WithOverrides` in `handlers_consult.go`). There is no code change
here. The delegation guide (`instructions/claude-coding.md` in ai-config)
drops "never pick a template or model yourself" for model and effort,
keeping role-based routing as the default.

## Out of scope

- Inheriting the caller's raw env vars beyond what its named environments
  define.
- Changing environment semantics for tasks or spawned agents.

## Compatibility

Leo has external users. This is a behaviour change for dispatches made from
agents that run under a non-default environment. Call it out in the release
notes. No config migration is needed, and `environments: []` restores the
old behaviour per call.

## Tests

- Resolution: template-only, caller-only, both with collision (caller
  wins), override replaces caller, `[]` override, unknown name rejected.
- Nested: grandchild inherits the child's recorded list.
- Continuation reuses the recorded list.
- Interactive argv: assert the tmux `-e` set, including blanks for
  caller-session keys (assert argv, per the mocked-exec-seam lesson), plus
  one live check with `tmux show-environment` on a real pane.
