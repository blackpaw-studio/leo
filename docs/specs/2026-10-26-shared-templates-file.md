# Shared templates file

## Goal

Share agent templates between machines (for example a file in a git repo) while
the rest of `leo.yaml` stays per-machine.

## Config

New optional top-level key in `leo.yaml`:

```yaml
templates_file: ~/dotfiles/leo/templates.yaml
```

- `~` is expanded; a relative path resolves against the directory holding `leo.yaml`.
- The shared file has a single top-level key, `templates:`, with the same schema as
  `leo.yaml`'s `templates:`. Any other top-level key is a load error naming the key.
- Unset means behaviour is unchanged. The key is additive, so no migration is needed.

## Merge

- Effective templates = shared templates, then local `leo.yaml` templates by name.
  A local template replaces a shared template of the same name entirely; fields
  are never merged.
- Each effective template records its source: `shared`, `local`, or `local`
  overriding `shared`.
- Merging happens inside `Load`/`LoadWithRevision`, before any caller sees the
  config, so `Validate()`, the daemon, CLI, and tasks see one templates map.
- `templates_file` set but the file is missing or unparseable is a load error that
  names the path. It is never silently skipped.
- Per-machine values belong in local `environments:` (referenced by name from shared
  templates) or in `~` paths. A shared template that references an environment the
  machine lacks fails `Validate()` with the existing error.

## Writes

- `Save` splits templates by source: shared-sourced templates are written to the
  shared file, everything else to `leo.yaml`. Each file uses the existing atomic
  write (symlinks preserved, owner preserved). The shared file keeps its existing
  mode; a new file is created at 0600.
- A file is rewritten only when its content changed, so editing a local template
  never touches the shared file (no git noise).
- Editing or deleting a shared template writes or removes it in the shared file.
  Editing a local override writes `leo.yaml`. Deleting a local override removes it
  from `leo.yaml`, which uncovers the shared template again.
- New templates go to `leo.yaml` by default. The web UI create form and
  `leo template` create gain a "shared" option, shown only when `templates_file` is set.
- The config revision (`If-Match`) hashes both files, so a `git pull` that changes
  the shared file makes a stale web edit fail its revision check.
- Leo never runs git. Committing, pushing, and pulling are the user's job.

## Visibility

- The web UI template list and editor show a source badge: `shared`, `local`, or
  `local · overrides shared`.
- `leo template list` gains a SOURCE column.

## Reload

- No file watcher. After a `git pull`, run the existing reload (`leo service reload`,
  web reload, or any web/API save). Docs say so.

## Out of scope

- Multiple shared files, field-level merge, and moving a template between files
  (users can delete it and recreate it in the other file).
- Sharing any section other than `templates:`.
- Preserving comments in either file (`leo.yaml` already loses them on save).

## Trust

The shared file has the same trust level as `leo.yaml`. Templates can set
`harness_options` (for example, bypass permissions), so only point
`templates_file` at a repo you control. Docs state this.

## Testing

- Load: merge order, whole-template override, source tagging, missing or invalid
  file error, unknown top-level key error, relative and `~` paths.
- Save round-trip: edit shared → only the shared file changes; edit local → only
  `leo.yaml` changes; delete override uncovers shared; new template `shared` option.
- Revision changes when only the shared file changes.
- Validate: a shared template referencing a missing environment errors with a
  `templates.<name>` path.
- Unset `templates_file`: existing load/save tests unchanged.
