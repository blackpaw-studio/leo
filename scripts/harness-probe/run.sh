#!/usr/bin/env bash
# Probe whether each coding harness honors MCP notifications/tools/list_changed.
#
#   run.sh [claude|codex|opencode|all]      (default: all)
#
# For each harness it launches the TUI interactively in a private tmux server
# (`tmux -L leo-probe`, the way leo spawns agents) with the probe MCP server
# wired in, sends a fixed prompt, and waits for the model to call probe_late —
# a tool that only exists after probe_ping triggers list_changed. A headless
# run (claude -p / codex exec / opencode run) is recorded as a secondary signal.
#
# Output: results/<UTC date>.json — one object per harness:
#   harness, version, model, client_info_name, relisted, called_late,
#   image_seen, honors (= relisted && called_late, interactive),
#   headless_honors (secondary)
# Feed `client_info_name` -> `honors` into internal/mcp/harnesscaps.go.
#
# Env: PROBE_WAIT (seconds to wait for probe_late, default 180),
#      PROBE_KEEP=1 keeps the work dirs, PROBE_MODEL_<HARNESS> overrides a model.
set -euo pipefail

# Re-exec under a scrubbed environment: this is usually run from inside a leo
# agent, whose LEO_*/CLAUDE_*/TMUX variables would leak into the probed
# harnesses (and could reach the live daemon). Auth locations are kept.
if [ -z "${LEO_PROBE_CLEAN:-}" ]; then
  exec env -i LEO_PROBE_CLEAN=1 HOME="$HOME" PATH="$PATH" TERM="${TERM:-xterm-256color}" \
    LANG="${LANG:-en_US.UTF-8}" USER="${USER:-}" SHELL="${SHELL:-/bin/sh}" TMPDIR="${TMPDIR:-/tmp}" \
    ${CLAUDE_CONFIG_DIR:+CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR"} ${CODEX_HOME:+CODEX_HOME="$CODEX_HOME"} \
    ${PROBE_WAIT:+PROBE_WAIT="$PROBE_WAIT"} ${PROBE_KEEP:+PROBE_KEEP="$PROBE_KEEP"} \
    ${PROBE_MODEL_CLAUDE:+PROBE_MODEL_CLAUDE="$PROBE_MODEL_CLAUDE"} ${PROBE_MODEL_CODEX:+PROBE_MODEL_CODEX="$PROBE_MODEL_CODEX"} \
    ${PROBE_MODEL_OPENCODE:+PROBE_MODEL_OPENCODE="$PROBE_MODEL_OPENCODE"} \
    "$0" "$@"
fi

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
WAIT="${PROBE_WAIT:-180}"
SOCK="leo-probe"
DATE="$(date -u +%Y-%m-%d)"
RESULTS="$HERE/results/$DATE.json"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/leo-probe.XXXXXX")"
PROMPT="call probe_ping; then re-check your tools; call probe_late if present; say the image colour"

tm() { tmux -L "$SOCK" "$@"; }
cleanup() {
  tm kill-server 2>/dev/null || true
  [ "${PROBE_KEEP:-0}" = 1 ] || rm -rf "$WORK"
}
trap cleanup EXIT

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v tmux >/dev/null || { echo "tmux is required" >&2; exit 1; }
TIMEOUT="$(command -v timeout || command -v gtimeout || true)"
[ -n "$TIMEOUT" ] || { echo "timeout (coreutils) is required" >&2; exit 1; }
go build -o "$WORK/probeserver" "$REPO/scripts/harness-probe/probeserver"

# harness_version <harness>
harness_version() {
  case "$1" in
    claude) claude --version | awk '{print $1}' ;;
    codex) codex --version | awk '{print $2}' ;;
    opencode) opencode --version ;;
  esac
}

# write_config <harness> <dir> <log> <tag> — wire the probe server (logging to
# <log>) into the harness config file for one run; prints that file's path.
write_config() {
  local h="$1" dir="$2" log="$3" tag="$4"
  case "$h" in
    claude)
      cat >"$dir/mcp.$tag.json" <<JSON
{"mcpServers":{"probe":{"command":"$WORK/probeserver","env":{"PROBE_LOG":"$log"}}}}
JSON
      echo "$dir/mcp.$tag.json" ;;
    opencode)
      cat >"$dir/opencode.$tag.json" <<JSON
{"mcp":{"probe":{"type":"local","command":["$WORK/probeserver"],"enabled":true,"environment":{"PROBE_LOG":"$log"}}},"permission":{"probe_*":"allow"}}
JSON
      echo "$dir/opencode.$tag.json" ;;
  esac
}

CODEX_MCP="-c 'mcp_servers.probe.command=\"$WORK/probeserver\"' -c 'mcp_servers.probe.env_vars=[\"PROBE_LOG\"]' -c 'mcp_servers.probe.default_tools_approval_mode=\"approve\"'"
CLAUDE_ALLOW="--allowedTools 'mcp__probe__probe_ping mcp__probe__probe_late'"

# launch_cmd <harness> <dir> <log> — the interactive shell command for tmux.
launch_cmd() {
  local h="$1" dir="$2" log="$3" model_var="PROBE_MODEL_$(printf %s "$1" | tr a-z A-Z)"
  local model="${!model_var:-}" cfg
  cfg="$(write_config "$h" "$dir" "$log" interactive)"
  case "$h" in
    claude) echo "claude --mcp-config $cfg --strict-mcp-config $CLAUDE_ALLOW ${model:+--model $model}" ;;
    codex) echo "PROBE_LOG=$log codex $CODEX_MCP ${model:+-m $model}" ;;
    opencode) echo "OPENCODE_CONFIG_CONTENT='$(jq -c . "$cfg")' opencode ${model:+-m $model}" ;;
  esac
}

# headless_cmd <harness> <dir> <log> — secondary signal (claude -p, codex exec,
# opencode run), logging to its own file.
headless_cmd() {
  local h="$1" dir="$2" log="$3" cfg
  cfg="$(write_config "$h" "$dir" "$log" headless)"
  case "$h" in
    claude) echo "claude -p \"$PROMPT\" --mcp-config $cfg --strict-mcp-config $CLAUDE_ALLOW" ;;
    codex) echo "PROBE_LOG=$log codex exec --skip-git-repo-check $CODEX_MCP \"$PROMPT\"" ;;
    opencode) echo "OPENCODE_CONFIG_CONTENT='$(jq -c . "$cfg")' opencode run \"$PROMPT\"" ;;
  esac
}

# wait_for_late <log> <seconds> — poll until a probe_late tools/call is logged.
wait_for_late() {
  local log="$1" deadline=$((SECONDS + $2))
  while [ $SECONDS -lt $deadline ]; do
    grep -q '"name":"probe_late"' "$log" 2>/dev/null && return 0
    sleep 2
  done
  return 1
}

# dismiss_dialogs <target> — accept first-run folder-trust prompts.
dismiss_dialogs() {
  local t="$1" i pane
  for i in $(seq 1 30); do
    pane="$(tm capture-pane -p -t "$t" 2>/dev/null || true)"
    if grep -q "Update now" <<<"$pane"; then
      # codex/opencode update prompts: default is "Update now" (runs brew).
      tm send-keys -t "$t" Down
      sleep 0.5
      tm send-keys -t "$t" Enter
      sleep 2
    elif grep -q "No, exit" <<<"$pane"; then
      # claude's folder-trust dialog defaults to "No, exit".
      tm send-keys -t "$t" Down
      sleep 0.5
      tm send-keys -t "$t" Enter
      sleep 2
    elif grep -qiE "trust (this|the contents)|Yes, (I|continue)" <<<"$pane"; then
      tm send-keys -t "$t" Enter
      sleep 2
    elif grep -qE "(❯|›|>) *$|Ask anything|Ask Codex|Message|tab agents|esc interrupt|\? for shortcuts" <<<"$pane"; then
      return 0
    fi
    sleep 2
  done
}

# summarize <log> — relisted/called_late/client_info_name as JSON.
summarize() {
  local log="$1"
  [ -s "$log" ] || { echo '{"client_info_name":null,"relisted":false,"called_late":false,"lists":0}'; return; }
  jq -s '{
    client_info_name: ((map(select(.kind=="initialize"))[0].client_info.name) // null),
    relisted: (map(select(.kind=="tools_list" and .after_notify and (.tools|index("probe_late")))) | length > 0),
    called_late: (map(select(.kind=="tools_call" and .name=="probe_late")) | length > 0),
    lists: (map(select(.kind=="tools_list")) | length)
  }' "$log"
}

probe_one() {
  local h="$1" dir="$WORK/$1" ilog="$WORK/$1/interactive.jsonl" hlog="$WORK/$1/headless.jsonl"
  mkdir -p "$dir"
  if ! command -v "$h" >/dev/null; then
    echo "{\"harness\":\"$h\",\"error\":\"not installed\",\"honors\":false}"
    return
  fi
  local version; version="$(harness_version "$h")"
  echo "probing $h $version ..." >&2

  local cmd; cmd="$(launch_cmd "$h" "$dir" "$ilog")"
  local session="probe-$h"
  tm new-session -d -s "$session" -x 200 -y 50 -c "$dir" "$cmd"
  dismiss_dialogs "$session"
  sleep 5 # let the TUI finish starting (MCP servers connect after first paint)
  tm send-keys -t "$session" -l "$PROMPT"
  sleep 1
  tm send-keys -t "$session" Enter
  # A TUI that was not ready swallows the Enter and leaves the prompt typed
  # in its box: press Enter again until the model has called a probe tool.
  local attempt
  for attempt in 1 2 3; do
    sleep 20
    grep -q '"kind":"tools_call"' "$ilog" 2>/dev/null && break
    tm send-keys -t "$session" Enter
  done
  wait_for_late "$ilog" "$WAIT" || true
  sleep 15 # let the model describe the image
  local pane; pane="$(tm capture-pane -p -S -200 -t "$session" || true)"
  printf '%s\n' "$pane" >"$HERE/results/$DATE.$h.pane.txt"
  tm kill-session -t "$session" 2>/dev/null || true

  local image_seen=false
  # The prompt and tool text never mention the colour, so any "red" in the
  # pane after the probe_late call is the model reading the image.
  if grep -qiw "red" <<<"$pane"; then image_seen=true; fi

  # Secondary signal: headless run, bounded so a hang can't stall the probe.
  local hcmd; hcmd="$(headless_cmd "$h" "$dir" "$hlog")"
  (cd "$dir" && "$TIMEOUT" "$WAIT" bash -c "$hcmd" >"$dir/headless.out" 2>&1 </dev/null) || true
  local hsum; hsum="$(summarize "$hlog")"
  local isum; isum="$(summarize "$ilog")"

  jq -n --arg h "$h" --arg v "$version" --argjson i "$isum" --argjson s "$hsum" --argjson img "$image_seen" '{
    harness: $h, version: $v,
    client_info_name: $i.client_info_name,
    relisted: $i.relisted, called_late: $i.called_late, image_seen: $img,
    honors: ($i.relisted and $i.called_late),
    headless_client_info_name: $s.client_info_name,
    headless_honors: ($s.relisted and $s.called_late)
  }'
}

targets=("${1:-all}")
[ "${targets[0]}" = all ] && targets=(claude codex opencode)

# Merge into an existing results file so probing one harness at a time
# accumulates, replacing only that harness's entry.
out="[]"
[ -s "$RESULTS" ] && out="$(cat "$RESULTS")"
for h in "${targets[@]}"; do
  case "$h" in claude|codex|opencode) ;; *) echo "unknown harness: $h" >&2; exit 2 ;; esac
  out="$(jq --argjson r "$(probe_one "$h")" 'map(select(.harness != $r.harness)) + [$r]' <<<"$out")"
done
printf '%s\n' "$out" >"$RESULTS"
echo "wrote $RESULTS" >&2
cat "$RESULTS"
