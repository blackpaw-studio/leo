# Attach picker sort modes

Date: 2026-09-05
Status: approved

## Goal

The `leo attach` picker offers three sort modes, remembers the last one used
across invocations, and defaults to "most recently attached by me".

## Sort modes

Cycle order on the `o` key: **Recent → Name → Uptime → Recent**.

- **Recent** (default on first run): agents this user has attached to, newest
  attach first, globally across hosts. Agents never attached follow, in
  today's order (local host first, then remote hosts by name, agents by
  display name within each host). On a fresh install Recent renders
  identically to the current list.
- **Name**: today's ordering, unchanged.
- **Uptime**: longest-running first (earliest `StartedAt`), globally across
  hosts. Agents with a zero `StartedAt` sort last, by display name.

Cycling re-sorts in place and keeps the cursor on the same agent. Sort
applies to the unfiltered set; the list filter narrows on top. While a filter
is being typed the list owns every key, so `o` is not intercepted.

The help footer shows the current mode, e.g. `o sort: recent`.

## Persistence

New package `internal/attachprefs` owning one file, `~/.leo/state/attach.json`:

```json
{
  "sort": "recent",
  "last_attached": { "local/vitals": "2026-09-05T23:10:00Z", "helios/build": "..." }
}
```

- Keys are `host/name`, matching the picker's existing `rowKey`. Local agents
  use the picker's `LocalHost` value.
- `Load(statePath)` returns zero-values (sort Recent, empty map) when the file
  is missing, unreadable, malformed, or names an unknown sort. It never
  returns an error to the caller; a preferences problem must not fail an
  attach.
- `Save` writes the whole file atomically (temp file + rename), `0600`.
- Update functions return a new value; the loaded value is not mutated.
- Entries for agents that no longer exist are kept. No pruning.

## Picker API

`picker.Run` gains an options value carrying the initial sort mode and the
last-attached map. The result reports the sort mode in effect when the picker
exited, including on quit. The picker never reads or writes disk.

## CLI wiring

- `runAttachPicker` loads prefs, passes them to the picker, and saves the
  returned sort mode after the picker returns regardless of how it exited.
- Every successful attach stamps `last_attached[host/name]` with the current
  time and saves, on both the picker path and the named path
  (`leo attach <name>`), local and remote.

## Testing

- `internal/picker`: ordering per mode including every tie-break above;
  cursor stays on the same agent across a cycle; filter typing does not
  consume `o`; result carries the final sort mode on attach and on quit.
- `internal/attachprefs`: load/save round-trip; missing, malformed, and
  unknown-sort files fall back to defaults; update returns a new value.
- `internal/cli`: sort mode saved after the picker returns; timestamp stamped
  on picker attach and on named attach, using the existing exec/picker seams.

## Out of scope

Last-activity ordering (needs daemon and remote-listing changes), sorting by
template or host, pruning stale timestamps, exposing the sort as a flag.
