#!/usr/bin/env bash
# Phase B: start an ISOLATED leo daemon over N synthetic dispatch records and
# record its startup step timings. Usage: run.sh <records> <burn-procs> [port]
# Never touches ~/.leo, launchd, or the production tmux socket.
set -u
RECORDS=${1:?records}; BURN=${2:-0}; PORT=${3:-18370}
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
LEO=${LEO_BIN:-$ROOT/bin/leo}
T=$(mktemp -d "${TMPDIR:-/tmp}/stall-repro.XXXXXX")
export LEO_HOME=$T HOME=$T/home TMUX_TMPDIR=$T/tmux
unset TMUX
mkdir -p "$HOME" "$TMUX_TMPDIR" "$T/ws"
cat > "$T/leo.yaml" <<Y
web:
  enabled: true
  bind: 127.0.0.1
  port: $PORT
Y
python3 "$HERE/gen.py" --home "$T" --count "$RECORDS" >"$T/gen.json" || exit 1
PIDS=()
for _ in $(seq 1 "$BURN"); do yes >/dev/null & PIDS+=($!); done
[ "$BURN" -gt 0 ] && sleep 20
echo "load before start: $(uptime | sed 's/.*averages: //')"
t0=$(date +%s)
(cd "$T/ws" && exec "$LEO" service --supervised -c "$T/leo.yaml") >"$T/service.log" 2>&1 &
D=$!
echo "$D" > "$T/daemon.pid"
echo "T=$T daemon pid=$D"
dumped=0
while kill -0 "$D" 2>/dev/null; do
  now=$(date +%s); el=$((now-t0))
  if curl -fsS -m 2 --unix-socket "$T/state/leo.sock" http://leo/health 2>/dev/null | grep -Eq '"ready": *true'; then break; fi
  if [ -n "${DUMP_AFTER:-}" ] && [ "$dumped" = 0 ] && [ "$el" -ge "$DUMP_AFTER" ]; then
    dumped=1; (cd "$T/ws" && "$LEO" service dump -c "$T/leo.yaml") >"$T/dump.out" 2>&1; echo "dump at ${el}s: $(cat "$T/dump.out")"
  fi
  [ "$el" -gt "${MAX_WAIT:-900}" ] && { echo "TIMEOUT after ${el}s"; break; }
  sleep 1
done
echo "ready after $(( $(date +%s)-t0 ))s; load after: $(uptime | sed 's/.*averages: //')"
echo "lock line in stderr log: $(grep -c 'startup: lock' "$T/service.log")  in state/service.log: $(grep -c 'startup: lock' "$T/state/service.log")"
grep -hE "startup:|slow record" "$T/state/service.log" | cut -c1-200
echo "--- cleanup"
kill "$D" 2>/dev/null; sleep 2; kill -9 "$D" 2>/dev/null
for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
TMUX_TMPDIR=$TMUX_TMPDIR tmux -L leo kill-server 2>/dev/null
echo "artifacts kept in $T"
