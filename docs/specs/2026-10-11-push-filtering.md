# Push notification filtering

## Goal

Let the operator choose which attention transitions push to paired iOS
devices, both daemon-wide and per template. Settings are editable in
`leo.yaml` and in the web UI, and take effect without a daemon restart.

## Config

A `push` block on `defaults` and on `templates.<name>`. Each attention state
is its own optional bool and cascades on its own:

```yaml
defaults:
  push:
    needs_input: true
    finished: true
    errored: true
templates:
  cron-bot:
    push:
      finished: false   # needs_input, errored inherit from defaults
```

- Type: `PushConfig{NeedsInput, Finished, Errored *bool}` (yaml
  `needs_input`, `finished`, `errored`), in `internal/config`.
- Resolution for agent A in state S: the value on A's template, then the
  value on `defaults`, then `true`. If you configure nothing, every state
  still pushes, same as today.
- Resolver: `(*Config).PushEnabled(template string, state string) bool`.
  An empty or unknown template name resolves from `defaults`.
- Validation: only YAML typing (bools). `config.Load` is a plain
  `yaml.Unmarshal`, so an unknown key under `push` is ignored like any other
  unknown key. No new `Validate` checks.

## Runtime

- `push.Notifier` takes an injected policy,
  `func(agent, state string) bool`, set through a `NotifierOption`.
  Without the option, every needs-you state sends, which keeps current tests
  and behavior.
- `observe()` still records every transition in `last`. When it sees a
  needs-you transition, it calls the policy and queues an alert only if the
  policy returns true. A suppressed transition still updates `last`, so it
  never causes a later push.
- `service.wirePush` builds the policy from the existing `cfgLoader` (a fresh
  `config.Load` on each call) plus an agentstore lookup that maps agent
  name → template. Transitions are rare, so reading per call is cheap and
  picks up web saves live.
- Agents without a template record (implicit persistent-task agents) use
  `defaults`. Dispatch subagents use their template like any other
  ephemeral agent.
- Failure: if config or agentstore can't be loaded, log it and **send**
  (fail open). Missing a "needs permission" push is worse than an extra
  one.

## Web UI

- **Defaults page:** a "Push notifications" group with three bool toggles
  (`KindBool`). Unset displays as on.
- **Template edit form:** a "Push notifications" group with three selects
  (`KindSelect`, options `inherit` / `on` / `off`). `inherit` removes the
  key from YAML; `on`/`off` set it explicitly.
- Fields are registered in `internal/web/schema/registry.go`, which
  satisfies the drift test. No new form kind.
- If the Defaults or Template forms aren't schema-driven, follow whatever
  pattern they use. The implementer confirms this.

## Out of scope

- Per-task overrides. Oneshot tasks never push, and persistent tasks follow
  their template or `defaults`.
- Per-device preferences, quiet hours, a global kill switch (unpairing
  covers that), and new attention states.

## Testing

- Resolver: table test covering template set, template unset → defaults,
  both unset → true, unknown template.
- Notifier: the policy blocks a state, so no alert is queued. A suppressed
  transition followed by the same state queues nothing. A different allowed
  state afterwards does queue. No policy means current behavior.
- wirePush policy: a config change between two transitions is honored
  without rebuilding (temp LEO home, no real relay). A load error sends.
- Web: saving the Defaults toggles and the Template selects round-trips
  through YAML, including `inherit` removing the key.

## Docs

`docs/configuration/config-reference.md` gets `push` rows under `defaults`
and templates.
