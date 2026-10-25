#!/usr/bin/env bash
# The end state a repro run (run.sh) left in <T>, as sorted JSON with
# timestamps and commit hashes dropped, so two runs over the same generated
# records (gen.py --seed) can be diffed. Usage: snapshot.sh <T>
set -euo pipefail
T=${1:?usage: snapshot.sh <T>}
REAL=$(cd "$T" && pwd -P)
TMPD=$(mktemp -d)
trap 'rm -rf "$TMPD"' EXIT
find "$T/state/dispatches" -name '*.json' -print0 | xargs -0 jq -S -c '{
  id, status, worktree_state, branch_tip: (.branch_tip != null), pane_id, viewer_pane_id, viewer_kind, error,
  notifications: ((.notifications // {}) | map_values(.disposition))
}' | jq -s 'sort_by(.id)' >"$TMPD/records.json"
git -C "$T/repo" worktree list --porcelain | sed -n 's/^worktree //p' |
  sed -e "s|^$REAL||" -e "s|^$T||" | sort | jq -R . | jq -s . >"$TMPD/worktrees.json"
git -C "$T/repo" branch --list 'leo/*' --format='%(refname:short)' | sort | jq -R . | jq -s . >"$TMPD/branches.json"
{ ls "$T/state/settings" 2>/dev/null || true; } | sort | jq -R . | jq -s . >"$TMPD/settings.json"
jq -S -n --slurpfile records "$TMPD/records.json" --slurpfile worktrees "$TMPD/worktrees.json" \
  --slurpfile branches "$TMPD/branches.json" --slurpfile settings "$TMPD/settings.json" \
  '{records: $records[0], worktrees: $worktrees[0], branches: $branches[0], settings: $settings[0]}'
