#!/usr/bin/env bash
# Smoke test for the published Leo image. Needs a docker engine and Go; no
# credentials. Everything runs inside throwaway containers/volumes.
#
#   A  fresh uid-1000 volume: starter config, claude.json seed, init.d order,
#      web port, tmux server, restart keeps config and re-runs init.d
#   B  a failing init.d script stops startup with its exit code
#   C  auth env (dummy values) reaches a spawned agent's process, and the
#      workspace-trust dialog does not stall it
#
# The daemon matches the Host header including its port, so requests use :8370.
# SKIP_BUILD=1 reuses $IMAGE as-is.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
IMAGE=${IMAGE:-leo-smoke:local}
RUN_ID=$$
PREFIX=leo-smoke-$RUN_ID
WORK=$(mktemp -d)
TIMEOUT=${SMOKE_TIMEOUT:-90}

log()  { printf '[smoke] %s\n' "$*" >&2; }
fail() { printf '[smoke] FAIL: %s\n' "$*" >&2; dump_logs; exit 1; }

dump_logs() {
  local c
  for c in $(docker ps -a --filter "name=$PREFIX" --format '{{.Names}}'); do
    printf '%s\n' "--- logs $c ---" >&2
    docker logs "$c" 2>&1 | tail -40 >&2 || true
  done
}

cleanup() {
  docker ps -aq --filter "name=$PREFIX" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker volume ls -q --filter "name=$PREFIX" | xargs -r docker volume rm -f >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

engine_arch() {
  case "$(docker info --format '{{.Architecture}}')" in
    aarch64|arm64) echo arm64 ;;
    x86_64|amd64) echo amd64 ;;
    *) fail "unsupported engine architecture" ;;
  esac
}

# build_image mirrors the build context goreleaser's dockers_v2 produces:
# <os>/<arch>/leo plus the extra_files at their repo-relative paths.
build_image() {
  local arch ctx=$WORK/ctx
  arch=$(engine_arch)
  mkdir -p "$ctx/linux/$arch" "$ctx/docker"
  log "building leo for linux/$arch"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$ctx/linux/$arch/leo" ./cmd/leo)
  cp "$ROOT/docker/entrypoint.sh" "$ROOT/docker/claude.seed.json" "$ctx/docker/"
  log "building image $IMAGE"
  docker build --platform "linux/$arch" -f "$ROOT/docker/Dockerfile" -t "$IMAGE" "$ctx"
}

# seed_volume <volume> <host-dir> <path-under-home> unpacks a host directory
# into the volume as uid 1000, preserving modes. It streams a tar so it works
# on engines that cannot bind-mount host paths (colima, remote daemons).
seed_volume() {
  tar -C "$2" -cf - . | docker run -i --rm --user 1000:1000 -v "$1:/home/leo" --entrypoint sh "$IMAGE" \
    -c "mkdir -p /home/leo/$3 && tar --warning=no-unknown-keyword -xf - -C /home/leo/$3"
}

host_port() { docker port "$1" 8370/tcp | head -1 | sed 's/.*://'; }

# wait_http <container> <host-header> waits for /login to answer 200.
wait_http() {
  local c=$1 host=$2 i port code
  for ((i = 0; i < TIMEOUT; i++)); do
    [[ $(docker inspect -f '{{.State.Running}}' "$c") == true ]] || fail "$c exited before the web port answered"
    port=$(host_port "$c" || true)
    if [[ -n $port ]]; then
      code=$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $host" "http://127.0.0.1:$port/login" || true)
      [[ $code == 200 ]] && return 0
    fi
    sleep 1
  done
  fail "web port never answered 200 (last code: ${code:-none})"
}

[[ ${SKIP_BUILD:-} == 1 ]] || build_image

# ---------------------------------------------------------------- phase A
log "phase A: fresh volume, uid 1000"
VOL_A=$PREFIX-a
docker volume create "$VOL_A" >/dev/null
seed_volume "$VOL_A" "$ROOT/docker/smoke/init.d" .leo/init.d
docker run -d --name "$PREFIX-a" --user 1000:1000 -v "$VOL_A:/home/leo" \
  -e LEO_ALLOWED_HOSTS=foo,localhost -p 127.0.0.1::8370 "$IMAGE" >/dev/null
wait_http "$PREFIX-a" localhost:8370

exec_a() { docker exec "$PREFIX-a" "$@"; }

cfg=$(exec_a cat /home/leo/.leo/leo.yaml)
grep -q 'bind: 0.0.0.0' <<<"$cfg" || fail "starter config lacks bind: 0.0.0.0"
grep -q 'port: 8370' <<<"$cfg" || fail "starter config lacks port: 8370"
grep -qE '^\s*-\s*foo\s*$' <<<"$cfg" || fail "allowed_hosts missing foo from LEO_ALLOWED_HOSTS"
grep -qE '^\s*-\s*localhost\s*$' <<<"$cfg" || fail "allowed_hosts missing localhost"
exec_a test -s /home/leo/.claude.json || fail "~/.claude.json was not seeded"
exec_a grep -q '"hasCompletedOnboarding": *true' /home/leo/.claude.json || fail "seed lacks hasCompletedOnboarding"
exec_a grep -q '"/home/leo"' /home/leo/.claude.json || fail "seed does not trust /home/leo"

exec_a grep -qx 'check_for_update_on_startup = false' /home/leo/.codex/config.toml || fail "codex config not seeded with updates off"

[[ $(exec_a head -1 /home/leo/init-marker) == "config=yes socket=absent" ]] \
  || fail "init.d must run after config seeding and before the daemon socket exists"
docker logs "$PREFIX-a" 2>&1 | grep -q '20-skipped.txt' || fail "non-executable init.d file not named in logs"

code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: foo:8370' "http://127.0.0.1:$(host_port "$PREFIX-a")/login")
[[ $code == 200 ]] || fail "Host: foo (from LEO_ALLOWED_HOSTS) got $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example:8370' "http://127.0.0.1:$(host_port "$PREFIX-a")/login")
[[ $code != 200 ]] || fail "unlisted Host header was accepted"

for cli in claude codex opencode gh mise; do
  exec_a "$cli" --version >/dev/null 2>&1 || fail "$cli --version failed in the image"
done
exec_a tmux -L leo ls >/dev/null || fail "tmux server (-L leo) is not up"
exec_a sh -c 'test "$(id -u)" = 1000' || fail "exec user is not uid 1000"

sum_before=$(exec_a sha256sum /home/leo/.leo/leo.yaml)
docker restart "$PREFIX-a" >/dev/null
wait_http "$PREFIX-a" localhost:8370
[[ $(exec_a sha256sum /home/leo/.leo/leo.yaml) == "$sum_before" ]] || fail "leo.yaml changed across restart"
[[ $(exec_a sh -c 'wc -l < /home/leo/init-marker') -eq 2 ]] || fail "init.d did not run again after restart"
log "phase A ok"

# ---------------------------------------------------------------- phase B
log "phase B: failing init.d script"
VOL_B=$PREFIX-b
docker volume create "$VOL_B" >/dev/null
seed_volume "$VOL_B" "$ROOT/docker/smoke/init-fail.d" .leo/init.d
set +e
docker run --name "$PREFIX-b" --user 1000:1000 -v "$VOL_B:/home/leo" "$IMAGE" >"$WORK/b.out" 2>&1
rc=$?
set -e
[[ $rc -eq 7 ]] || fail "failing init.d script: container exited $rc, want 7"
grep -q 'failing-on-purpose' "$WORK/b.out" || fail "failing script's output not in container logs"
docker run --rm --user 1000:1000 -v "$VOL_B:/home/leo" --entrypoint sh "$IMAGE" \
  -c 'test ! -S /home/leo/.leo/state/leo.sock' || fail "daemon started despite failing init.d script"
log "phase B ok"

# ---------------------------------------------------------------- phase C
log "phase C: auth env reaches an agent; no trust dialog"
VOL_C=$PREFIX-c
docker volume create "$VOL_C" >/dev/null
mkdir -p "$WORK/c/.leo"
cp "$ROOT/docker/smoke/leo.yaml" "$WORK/c/.leo/leo.yaml"
seed_volume "$VOL_C" "$WORK/c" .
docker run -d --name "$PREFIX-c" --user 1000:1000 -v "$VOL_C:/home/leo" \
  -e CLAUDE_CODE_OAUTH_TOKEN=smoke-oauth -e ANTHROPIC_API_KEY=smoke-anthropic \
  -e OPENAI_API_KEY=smoke-openai -p 127.0.0.1::8370 "$IMAGE" >/dev/null
wait_http "$PREFIX-c" localhost:8370
docker exec "$PREFIX-c" leo agent spawn probe >/dev/null || fail "leo agent spawn probe failed"

missing=unset
for ((i = 0; i < TIMEOUT; i++)); do
  # The agent may crash on dummy credentials and be restarted by the
  # supervisor, so re-resolve the pane pid on every attempt.
  pid=$(docker exec "$PREFIX-c" tmux -L leo list-panes -a -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  if [[ -n $pid ]]; then
    env_dump=$(docker exec "$PREFIX-c" sh -c "tr '\\0' '\\n' < /proc/$pid/environ" 2>/dev/null || true)
    missing=""
    for kv in CLAUDE_CODE_OAUTH_TOKEN=smoke-oauth ANTHROPIC_API_KEY=smoke-anthropic OPENAI_API_KEY=smoke-openai; do
      grep -qxF "$kv" <<<"$env_dump" || missing+=" ${kv%%=*}"
    done
    [[ -z $missing && -n $env_dump ]] && break
  fi
  sleep 1
done
[[ -z $missing ]] || fail "agent process env is missing:${missing:- (no pane found)}"

pane=$(docker exec "$PREFIX-c" tmux -L leo capture-pane -p -t "$(docker exec "$PREFIX-c" tmux -L leo list-panes -a -F '#{pane_id}' | head -1)" || true)
! grep -qi 'is this a project you created or one you trust' <<<"$pane" || fail "agent stalled on the workspace trust dialog"
log "phase C ok"
log "all phases passed"
