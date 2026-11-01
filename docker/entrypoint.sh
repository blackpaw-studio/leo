#!/usr/bin/env bash
# Leo container entrypoint. On every start:
#   1. write a starter ~/.leo/leo.yaml if there is none (never overwritten)
#   2. seed ~/.claude.json if there is none, so a fresh volume does not stall
#      agents on Claude's first-run screens
#      Codex has no env switch for its updater, so ~/.codex/config.toml is
#      seeded the same way (only if missing) with the update check off
#      On every start, ANTHROPIC_API_KEY is approved in ~/.claude.json, and
#      OPENAI_API_KEY logs Codex in when it has no credentials yet
#   3. run each executable in ~/.leo/init.d/ in lexical order, as this user;
#      a non-zero exit aborts startup with that code
#   4. exec `leo service` in the foreground
set -euo pipefail

LEO_DIR="$HOME/.leo"
CONFIG="$LEO_DIR/leo.yaml"
INIT_DIR="$LEO_DIR/init.d"
SEED=/usr/local/share/leo/claude.seed.json
CODEX_CONFIG="$HOME/.codex/config.toml"
HOST_PATTERN='^[][A-Za-z0-9.:-]+$'

log() { printf '[leo-entrypoint] %s\n' "$*" >&2; }

# allowed_hosts_yaml turns a comma-separated host list into YAML list items,
# rejecting anything that is not a plain hostname or IP.
allowed_hosts_yaml() {
  local raw=${LEO_ALLOWED_HOSTS:-localhost,127.0.0.1} host
  local -a hosts
  IFS=',' read -ra hosts <<<"$raw"
  for host in "${hosts[@]}"; do
    host=${host//[[:space:]]/}
    [[ -n $host ]] || continue
    if [[ ! $host =~ $HOST_PATTERN ]]; then
      log "invalid host in LEO_ALLOWED_HOSTS: '$host' (bare hostnames or IPs only, comma-separated)"
      exit 64
    fi
    printf '    - "%s"\n' "$host"
  done
}

write_starter_config() {
  local hosts tmp
  hosts=$(allowed_hosts_yaml)
  if [[ -z $hosts ]]; then
    log "LEO_ALLOWED_HOSTS has no usable entries"
    exit 64
  fi
  mkdir -p "$LEO_DIR"
  tmp=$(mktemp "$LEO_DIR/.leo.yaml.XXXXXX")
  cat >"$tmp" <<YAML
# Starter config written by the Leo container entrypoint. Edit freely: it is
# only written when this file is missing.
web:
  enabled: true
  bind: 0.0.0.0
  port: 8370
  allowed_hosts:
$hosts
YAML
  mv "$tmp" "$CONFIG"
  log "wrote starter config $CONFIG"
}

# approve_anthropic_key marks ANTHROPIC_API_KEY as already approved so Claude
# does not stall on its "use this API key?" dialog. Claude identifies a key by
# its last 20 characters. Runs on every start; adds the entry once, never
# removes others, never prints the key.
approve_anthropic_key() {
  local key=${ANTHROPIC_API_KEY:-} state="$HOME/.claude.json" tmp
  [[ -n $key ]] || return 0
  tmp=$(mktemp "$HOME/.claude.json.XXXXXX")
  if jq --arg k "${key: -20}" '
        .customApiKeyResponses = ((.customApiKeyResponses // {})
          | .approved = ((.approved // []) as $a | if ($a | index($k)) then $a else $a + [$k] end))
      ' "$state" >"$tmp" 2>/dev/null; then
    mv "$tmp" "$state"
  else
    rm -f "$tmp"
    log "could not update $state to approve ANTHROPIC_API_KEY; Claude may ask about the key"
  fi
}

# login_codex_with_api_key logs Codex in from OPENAI_API_KEY, whose TUI ignores
# the variable unless logged in. Existing credentials are never replaced and
# nothing from the login (which could echo the key) reaches the logs.
login_codex_with_api_key() {
  [[ -n ${OPENAI_API_KEY:-} && ! -f $HOME/.codex/auth.json ]] || return 0
  if printenv OPENAI_API_KEY | codex login --with-api-key >/dev/null 2>&1; then
    log "logged Codex in with OPENAI_API_KEY"
  else
    log "codex login with OPENAI_API_KEY failed; run 'codex login' in the container"
  fi
}

run_init_scripts() {
  local f rc
  [[ -d $INIT_DIR ]] || return 0
  shopt -s nullglob
  for f in "$INIT_DIR"/*; do
    [[ -f $f ]] || continue
    if [[ ! -x $f ]]; then
      log "skipping non-executable init script: $(basename "$f")"
      continue
    fi
    log "running init script: $(basename "$f")"
    "$f" && rc=0 || rc=$?
    if ((rc != 0)); then
      log "init script $(basename "$f") exited $rc; aborting startup"
      exit "$rc"
    fi
  done
}

[[ -f $CONFIG ]] || write_starter_config
[[ -f $HOME/.claude.json ]] || cp "$SEED" "$HOME/.claude.json"
if [[ ! -f $CODEX_CONFIG ]]; then
  mkdir -p "$(dirname "$CODEX_CONFIG")"
  printf 'check_for_update_on_startup = false\n' >"$CODEX_CONFIG"
fi
approve_anthropic_key
login_codex_with_api_key
mkdir -p "$INIT_DIR"
run_init_scripts

exec leo service
