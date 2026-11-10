# Plan: sync appearance_rules through follow-lead

Spec: `docs/specs/2026-11-09-follow-appearance-rules.md`. I checked every hook in the brief against the code; all of them exist.

## 1. Ordered steps
Each step starts with a failing test (RED), then the change.

1. **Tell a missing key from an empty list** (`internal/config/config.go:112`, `appearance.go`)
   - Change the field to `AppearanceRules *AppearanceRuleList` with the yaml tag `appearance_rules,omitempty`. A nil pointer means the key is absent, and so does `appearance_rules: null`, which matches how follow treats a null local key. A non-nil pointer to an empty list means `[]`. yaml.v3's omitempty only drops a nil pointer, so `[]` survives `Save`, `saveFollower` and `marshalLocal` with no custom code.
   - Add `func (c *Config) LocalAppearanceRules() (AppearanceRuleList, bool)`.
   - RED: `TestAppearanceRulesPresenceRoundTrip` in `internal/config`. For absent, `[]` and a populated list it checks Load → Save → Load on a plain host, on a follower without a cache and on a follower with a cache.
   - The 30 test references to the old field (`config/appearance_test.go`, `web/appearance_*_test.go`, `agent/appearance_events_test.go`) need mechanical fixes. The compiler finds every call site, including the ones that should switch to the effective list.
   - Verify: `go test -race ./internal/config/`
2. **Export the key** (`follow_export.go`)
   - Add `AppearanceRules any` with the json tag `appearance_rules` and no omitempty. `BuildFollowExport` sets it with `toJSONValue` on the raw local list, and an absent key becomes `[]any{}`.
   - `Digest()` gains `appearance_rules,omitempty` and maps an empty or absent list to nil. Old leads and leads with no rules then keep their current digest, so upgrading triggers no spurious pull change.
   - RED, in `internal/config`:
     - `TestFollowExportAppearanceRulesKeyAlwaysPresent`: the key is there with no rules, with an empty list and with a populated list.
     - `TestFollowDigestCoversAppearanceRules`: the digest changes when a rule changes and stays the same between absent and `[]`.
3. **Layer the lead's rules on the follower** (`follow_layer.go`, `appearance.go`)
   - `followLayer` gains `leadAppearance AppearanceRuleList`.
   - In `applyFollowLayer`, after the cache passes its checks, call a new `decodeLeadAppearance(exp.AppearanceRules)`, which runs `jsonToNode` and then the lenient `AppearanceRuleList` decode.
   - If that decode fails, the layer warns ("lead appearance_rules unusable; using none") and uses no rules. It never touches the rest of the layer.
   - The decode stays out of `decodeSections` on purpose, so a bad lead rule can't make the whole pull malformed. That is the spec's "a bad lead rule never blocks the follower".
   - Add two methods:
     - `func (c *Config) EffectiveAppearanceRules() AppearanceRuleList`: the local list if the key is present, else the lead's list if following, else nil.
     - `func (c *Config) AppearanceRulesSource() string`: returns `"lead"` when `c.Follow != nil` and there is no local key, otherwise `"local"`. Constants: `AppearanceRulesSourceLead` and `AppearanceRulesSourceLocal`.
   - `AppearanceRuleWarnings()` switches to the effective list.
   - RED: a table test `TestFollowAppearanceRulesLayering` in `internal/config` covering every layering case in the spec's Testing list, with a warning on an invalid lead rule. Add one case where the lead's value isn't a list at all, and check that the load still succeeds with templates intact.
4. **Consumers read the effective list**
   - `internal/agent/appearance.go:134` changes to `rules = cfg.EffectiveAppearanceRules()`. The snapshot reaches it through `AgentAppearance` (`web/handlers_observe.go:555`).
   - RED: `TestAgentAppearanceUsesLeadRuleOnFollower` in `internal/agent`, which writes a follower config and a cache.
   - Verify: `go test -race ./internal/agent/`
5. **GET and PUT** (`handlers_appearance.go`)
   - The document gains `Source string` with the json tag `source`. `newAppearanceRulesDocument(cfg *Config, rev)` builds it from `EffectiveAppearanceRules()` and `AppearanceRulesSource()`.
   - PUT keeps its body and sets `cfg.AppearanceRules = &rules`. An empty body list therefore writes `appearance_rules: []`, which counts as a local override.
   - `CheckLeadOwnership` only walks templates, environments and profiles, so PUT is never refused on a follower. A test pins this.
   - `revision`: a follower's revision already folds in `fc.Digest` through `revisionWithFollow` (`config.go:1030`). Step 2 adds the rules to `Digest`, so a pull that changes them changes the GET revision.
   - RED, in `internal/web/appearance_routes_test.go`:
     - `TestAppearanceRulesGetSource`, table-driven over: not following, follower with no local key, follower with a local list, follower with a local `[]`.
     - `TestAppearanceRulesPutOnFollowerCreatesOverride`
     - `TestAppearanceRulesRevisionChangesOnLeadPull`: write a new cache with a different digest and expect a new GET revision.
6. **New DELETE route** (`/appearance/rules` in `config_api.go:46`)
   - `handleAppearanceRulesDelete` reuses `ifMatchOf`: 428 without `If-Match`, 409 when stale.
   - It calls `Mutate`. When the local key is absent the apply step returns `errNoWrite`, so nothing is saved and the response is a 200 with the current document. Otherwise it sets `cfg.AppearanceRules = nil`.
   - It returns the GET body with an `ETag`.
   - RED: `TestAppearanceRulesDelete`. Cases: a follower reverts to `source: lead`; a plain host ends with `[]` and `source: local`; a missing `If-Match` gives 428; with no local key the response is 200 and leo.yaml is byte-identical.
7. **Wire golden**
   - Regenerate `internal/observe/testdata/wire/appearance-rules.json`, which gains `"source": "local"`.
   - Add `appearance-rules-lead.json` for the follower case.
   - Verify: `go test -race ./internal/web/ ./internal/observe/`
8. **Gate**: `make lint && make test`. Also run `make e2e` through `env -i … TMUX_TMPDIR=$(mktemp -d) make e2e`, since this changes config load and save.

## 2. Interfaces
- **Config:** `AppearanceRules *AppearanceRuleList`, YAML key `appearance_rules`. `nil` means absent, `&[]` means an empty override. The JSON name is unchanged; `env_roundtrip.json.golden` stays `null`.
- **Config methods:** `LocalAppearanceRules() (AppearanceRuleList, bool)`, `EffectiveAppearanceRules() AppearanceRuleList`, `AppearanceRulesSource() string`.
- **FollowExport:** `AppearanceRules any` with json `appearance_rules`, always emitted, `[]` when empty. `FollowExportSchema` stays at 2.
- **HTTP:** GET, PUT and DELETE on `/appearance/rules` return `{revision, rules, errors, source: "lead"|"local"}`. DELETE requires `If-Match`.

## 3. Docs
- `docs/configuration/follow-lead.md`: `appearance_rules` is synced. A local key, even an empty list, wins whole. A lead without the key counts as having no rules.
- `docs/configuration/appearance.md`: what `source` means, PUT on a follower creating an override, and DELETE reverting.
- `docs/configuration/config-api.md` (~235-248): the `source` field and the DELETE route, including If-Match/428 and the no-op 200.
- `docs/specs/2026-07-31-observability-api.md` (~383-399): `source` in the rules document, and that `rule:<i>` indexes the effective list.

## 4. Open questions — decisions the spec didn't settle
1. **Chained leads can't happen today.** `BuildFollowExport` refuses a lead that is itself a follower with `lead_is_follower` (`follow_export.go:83`), so the spec's chained-lead export test can't be written. The plan exports the lead's local list, which equals its effective list because it isn't a follower. The export test only covers the plain lead. The spec line should be corrected or dropped.
2. **There is no rules view in the UI or CLI to add `source` to.** The web UI has no appearance-rules page, and `leo config show` only dumps raw config under `--json`. `leo validate` prints the effective list's warnings. The plan adds no new UI. If you want `source` in `config show` text output, that's new scope; I'd leave it as a follow-up.
3. **The follower revision uses `fc.Digest`, which is stored at pull time.** A follower upgraded while holding a cache from a new lead won't fold the lead's rules into its revision until the next pull. That pull then reports `changed` once. The rules themselves apply right away, so this is accepted risk; no fix planned.
4. **A lead `appearance_rules` value that isn't a list is skipped, not marked malformed.** The lenient decode keeps it as one malformed entry that resolution skips, so it shows up as a warning. That is consistent with the spec.
