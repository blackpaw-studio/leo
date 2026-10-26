# Plan: shared templates file

**Spec:** `docs/specs/2026-10-26-shared-templates-file.md`. The spec wins wherever this plan disagrees with it.

## Design decisions

**1. Where source tracking lives.** Add one unexported pointer field to `Config`: `shared *sharedTemplates`. yaml.v3 and encoding/json both skip unexported fields, so nothing reaches the wire. Shallow copies keep the pointer, which is what's needed: `next := *cfg` in `rename.go:143`, `*cfg = *renamed` in `config_cores.go:254`, and `updated := *cfg` in `cli/web_enable.go:54`.
```go
type sharedTemplates struct {
    path      string                    // resolved templates_file
    raw       map[string]TemplateConfig // shared file as parsed, NOT expanded (written back as-is)
    baseline  map[string]TemplateConfig // second parse, expanded (change detection)
    sources   map[string]TemplateSource // computed at load
    newShared map[string]bool           // names created/renamed into the shared file this session
    leoBytes  []byte                    // leo.yaml bytes as loaded (lazy local baseline)
}
type TemplateSource string // "local" | "shared" | "local-override"; Label() → "local · overrides shared"
func (c *Config) TemplateSource(name string) TemplateSource
func (c *Config) HasTemplatesFile() bool
```
- `raw`, `baseline` and the effective map must be separate parses. `expandPaths` rewrites `AddDirs[i]` in place, and `schema.Apply` can change `Env` and `HarnessOptions` maps in place. If any of them shared backing storage with the snapshot, an edit would compare as unchanged and be lost.

**2. "Unchanged" means unchanged after parsing, not byte-equal.** The shared file will be hand-written: 2-space indents and comments. yaml.v3 marshals with 4-space indents, so comparing bytes would rewrite the file on every save and create git noise. Instead:
- **Shared file:** a template counts as changed when `yaml.Marshal(effective[n]) != yaml.Marshal(baseline[n])`, or when a name was added or removed. Unchanged templates are written back from `raw`. Changed ones are written with `contractHome`, which turns `$HOME/…` back into `~/…` in `Workspace`, `MCPConfig` and `AddDirs`; otherwise one machine's expanded paths would end up in a file other machines read.
- **leo.yaml:** compare `marshal(localView(cfg))` with `marshal(localView(parse(leoBytes)))`. Skip the write when they match.

**3. Revision.**
- With `templates_file` unset: `RevisionOf(leoBytes)`, exactly as today. The existing exact-digest test stays green.
- With it set: an unexported `revisionOfFiles(leo, shared)`, which is sha256 over `sha256(leo) ‖ sha256(shared)` with the `sha256:` prefix. It is computed inside `LoadWithRevision` from the same bytes it parsed.

**4. Unset `templates_file`, or a `cfg` that was not loaded from disk (`shared == nil`).** `Save` takes today's code path unchanged.

## Steps (TDD: write the named test, watch it fail, implement, rerun)

**Step 1: Load, merge and source tagging.** New `internal/config/shared_templates.go` and its test file. `config.go` gains the field `TemplatesFile string \`yaml:"templates_file,omitempty"\``, and the shared-file load is hooked into `LoadWithRevision` after `parse`.
- Functions:
  - `resolveTemplatesFile(home, p string) string`: expands `~`; a relative path joins `cfg.HomePath`.
  - `readSharedTemplates(path) (raw []byte, tmpls map[string]TemplateConfig, err error)`: unmarshals into `map[string]yaml.Node`, rejects any key other than `templates`, then decodes `templates`.
  - `mergeTemplates`.
- Use a top-level `map[string]yaml.Node`, not `KnownFields(true)`. `KnownFields` would also reject unknown fields inside templates, which `leo.yaml` tolerates.
- Errors are wrapped as `templates_file <path>: …`.
- Test `TestLoadSharedTemplates`, table-driven, with these cases:
  - merge order;
  - a local template replaces the shared one whole: shared `{model: opus, workspace: ~/w}` plus local `{model: haiku}` gives an empty `Workspace`;
  - sources come out as `shared`, `local` and `local-override`;
  - a missing file and an invalid YAML file both error, and the error contains the path;
  - `extra: 1` errors with a message containing `"extra"`;
  - a relative path resolves against the directory holding leo.yaml;
  - `~/t.yaml` resolves under the testenv `HOME`;
  - unset gives `cfg.Templates` identical to today.
- Test `TestValidateSharedTemplateMissingEnvironment`: the error message contains `templates.<name>.environments`.
- Verify: `go test -race -run 'TestLoadSharedTemplates|TestValidateShared' ./internal/config/`

**Step 2: Revision over both files.** `LoadWithRevision` and `revisionOfFiles` in `config.go`.
- Test `TestLoadWithRevisionChangesWhenOnlySharedFileChanges`: rewrite only the shared file, and the revision must change.
- `TestLoadWithRevisionHashesTheParsedBytes` must stay green unmodified.
- Verify: `go test -race -run TestLoadWithRevision ./internal/config/`

**Step 3: Creating a template as shared.**
- `func (c *Config) AddTemplate(name string, t TemplateConfig, shared bool) error`. It errors on a duplicate name, and on `shared && !HasTemplatesFile()` with "templates_file is not set".
- Tests:
  - `TestAddTemplateShared`: `TemplateSource("n") == "shared"`;
  - `TestAddTemplateSharedWithoutTemplatesFileErrors`;
  - `TestAddTemplateDefaultsLocal`.
- Verify: `go test -race -run TestAddTemplate ./internal/config/`

**Step 4: Save splits by source.** `Save` in `config.go` branches to a new `saveSplit(path, cfg)` in `shared_templates.go`.
- `atomic.go` grows `writeFileAtomicWith(path, data, keepMode bool)`; the existing `writeFileAtomic` delegates to it with `keepMode=false`. With `keepMode`, the existing target's mode is kept; a new file gets 0600.
- Shared-file assembly, for each name in `raw`:
  - source `local-override`: keep the `raw` entry;
  - source `shared` and still present: write `raw` if unchanged, otherwise `contractHome(effective)`;
  - source `shared` and gone: drop it.
  - Then add every name in `newShared`.
- Tests in `save_shared_test.go`. "Untouched" is checked with `os.SameFile` before and after, because an atomic write installs a new inode.
  - `TestSaveSplitEditSharedOnlyTouchesSharedFile`: leo.yaml untouched.
  - `TestSaveSplitEditLocalLeavesHandFormattedSharedFileUntouched`: the shared file has comments and 2-space indents.
  - `TestSaveSplitDeleteSharedRemovesFromSharedFile`.
  - `TestSaveSplitDeleteOverrideUncoversShared`: reload, and the source is `shared` with the shared field values.
  - `TestSaveSplitNewTemplateGoesLocal` and `TestSaveSplitNewSharedTemplateGoesShared`.
  - `TestSaveSplitKeepsSharedFileModeAndSymlink`: 0644 stays 0644, and a symlink stays a symlink.
  - `TestSaveSplitEditedSharedKeepsTildePaths`: the file contains `workspace: ~/w`, not an absolute path.
- The full existing `save_test.go` and `config_test.go` must stay green.
- Verify: `go test -race ./internal/config/`

**Step 5: Rename keeps the file.** Decided in open question 1; confirm with Evan.
- `RenameTemplate` in `rename.go`: when `old` is sourced `shared`, mark `new` in `newShared`. An override renames to a plain local template, so the shared template under the old name becomes active again.
- Test `TestRenameSharedTemplateStaysInSharedFile`: Save, reload, and check the source and which file holds the template. A second case covers renaming an override.
- Verify: `go test -race -run TestRename ./internal/config/`

**Step 6: Web revision after a write.**
- `internal/web/config_cores.go` `currentRevision`: reread with `config.LoadWithRevision` instead of hashing only leo.yaml.
- Test `TestMutateRevisionCoversSharedFile` in `config_cores_test.go`:
  - the revision a write returns equals what `LoadWithRevision` returns afterwards (this fails today);
  - and an `IfMatch` taken before the shared file changes on disk returns a 409 `revision_mismatch`.
- Verify: `go test -race -run TestMutateRevision ./internal/web/`

**Step 7: Web UI.**
- Files: `handlers_pages.go` (`templateRow.Source`, `templatesPageData.HasTemplatesFile`, `templateEditData.Source`), `config_templates.html`, `template_edit.html`, `handlers.go` `handleTemplateAdd` (passes `extra{"shared": r.FormValue("shared")}`), and the templates `add` in `config_resources.go`, which calls `cfg.AddTemplate`; `shared` without `templates_file` is a 422 on `shared`.
- Tests in `handlers_templates_test.go`:
  - `TestPageConfigTemplatesShowsSourceBadges`: the body contains `shared` and `local · overrides shared`;
  - `TestPageConfigTemplatesSharedOptionOnlyWithTemplatesFile`: the checkbox is absent when unset and present when set;
  - `TestTemplateAddSharedWritesSharedFile`;
  - `TestTemplateEditShowsSourceBadge`.
- Badge styling is UI and exempt from TDD; check it with a screenshot at the gate.
- Verify: `go test -race -run 'TestPageConfigTemplates|TestTemplateAdd|TestTemplateEdit' ./internal/web/`

**Step 8: CLI.**
- `internal/cli/template.go`:
  - list gains a SOURCE column, plus `Source` (machine value) in `templateListEntry` for `--json`;
  - add gains `--shared`, which calls `cfg.AddTemplate`.
- Tests in `template_test.go`: `TestTemplateList_ShowsSourceColumn`, `TestTemplateList_JSONIncludesSource`, `TestTemplateAdd_SharedWritesSharedFile`, `TestTemplateAdd_SharedWithoutTemplatesFileErrors`.
- Verify: `go test -race -run 'TestTemplateList|TestTemplateAdd' ./internal/cli/`

**Step 9: Docs.** No tests.
- `docs/configuration/config-reference.md` gets a `templates_file` section covering: path rules, merge and override, the source badges, writes and the `--shared` option, reloading after `git pull` (`leo service reload`, web reload, or any save), that leo never runs git, and the trust warning about `harness_options`.
- `config-api.md` gets one line saying the revision covers both files.

**Step 10: Integration gate.** Run on Dionysus via `heavy`, in the background:
- `heavy make test`
- `make lint`
- `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) heavy make e2e` (load changes count as config changes, and the e2e suite sits behind a build tag)
- An isolated test daemon (separate `LEO_HOME`; no production restart): the templates page in a screenshot, then edit a local template and confirm `git status` stays clean in a scratch repo holding the shared file.

## Risks
- **Two files are written without a transaction.** A rename that touches both the shared file (the template) and leo.yaml (task references) can leave a dangling reference if the second write fails. Both directions fail the same way, and failures are rare (disk full, permissions). Accept it, and name the failing file in the error.
- **`contractHome` rewrites absolute paths under `$HOME` as `~/…`** when a shared template is edited. On that machine the two mean the same thing.
- **Renaming a shared template cascades into task references in this machine's leo.yaml only.** Other machines' tasks that use the old name fail `Validate` after their next pull. That's inherent to the design; document it.
- **`config.Load` reads two files** on hot paths (observe handlers, push policy). The cost is small, and only applies when the key is set.
- **The 409 message still says "leo.yaml changed since it was read".** It is now slightly wrong when only the shared file changed. The code is unchanged, so clients that match on it are unaffected.

## Open questions (the spec doesn't settle these)
1. **Rename.** I recommend that renaming a shared template keeps it in the shared file (Step 5). Without it, a rename silently moves the template into leo.yaml, which is the "moving between files" the spec rules out.
2. **Config JSON API.** It does not expose `source` and its add body does not take `shared`; only the HTML form and the CLI do, as the spec states. Adding them for leoterm would be a follow-up.
3. **Duplicate.** The copy of a shared template goes to leo.yaml, following the "new templates go local" default. Confirm.

## Orchestrator decisions on open questions
1. Rename keeps a shared template in the shared file (Step 5 as written).
2. JSON config API `source`/`shared` exposure is a follow-up, not in this change.
3. Duplicating a shared template creates a local copy in leo.yaml.
