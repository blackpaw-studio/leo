#!/usr/bin/env bash
# Phase B: start an ISOLATED leo daemon over N synthetic dispatch records and
# record its startup step timings. Usage: run.sh <records> <burn-procs> [port]
# WAIT_FOR_LOG=<regex>: after ready, wait (under MAX_WAIT) for a matching
# line in state/service.log before tearing down.
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
# Not `seq 1 "$BURN"`: BSD seq counts down, so BURN=0 would start two.
for ((i = 0; i < BURN; i++)); do yes >/dev/null & PIDS+=($!); done
[ "$BURN" -gt 0 ] && sleep 20
echo "load before start: $(uptime | sed 's/.*averages: //')"
now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time * 1000'; }
secs() { perl -e 'printf "%.1f", $ARGV[0] / 1000' "$1"; }
t0=$(now_ms)
(cd "$T/ws" && exec "$LEO" service --supervised -c "$T/leo.yaml") >"$T/service.log" 2>&1 &
D=$!
echo "$D" > "$T/daemon.pid"
echo "T=$T daemon pid=$D"
dumped=0; web_ms=""
while kill -0 "$D" 2>/dev/null; do
  el_ms=$(( $(now_ms) - t0 )); el=$((el_ms / 1000))
  # Any HTTP answer at all: the web UI is listening.
  if [ -z "$web_ms" ] && curl -s -o /dev/null -m 1 "http://127.0.0.1:$PORT/" 2>/dev/null; then web_ms=$el_ms; fi
  if curl -fsS -m 2 --unix-socket "$T/state/leo.sock" http://leo/health 2>/dev/null | grep -Eq '"ready": *true'; then
    # Ready can land between probes: the web UI answering now still counts.
    if [ -z "$web_ms" ] && curl -s -o /dev/null -m 1 "http://127.0.0.1:$PORT/" 2>/dev/null; then web_ms=$(( $(now_ms) - t0 )); fi
    break
  fi
  if [ -n "${DUMP_AFTER:-}" ] && [ "$dumped" = 0 ] && [ "$el" -ge "$DUMP_AFTER" ]; then
    dumped=1; (cd "$T/ws" && "$LEO" service dump -c "$T/leo.yaml") >"$T/dump.out" 2>&1; echo "dump at ${el}s: $(cat "$T/dump.out")"
  fi
  [ "$el" -gt "${MAX_WAIT:-900}" ] && { echo "TIMEOUT after ${el}s"; break; }
  sleep 0.2
done
echo "ready after $(secs $(( $(now_ms) - t0 )))s; web UI listening after ${web_ms:+$(secs "$web_ms")}s (polled); load after: $(uptime | sed 's/.*averages: //')"
if [ -n "${WAIT_FOR_LOG:-}" ]; then
  while kill -0 "$D" 2>/dev/null && ! grep -Eq "$WAIT_FOR_LOG" "$T/state/service.log" 2>/dev/null; do
    [ $(( ($(now_ms) - t0) / 1000 )) -gt "${MAX_WAIT:-900}" ] && { echo "TIMEOUT waiting for /$WAIT_FOR_LOG/"; break; }
    sleep 0.2
  done
  echo "/$WAIT_FOR_LOG/ logged after $(secs $(( $(now_ms) - t0 )))s; load: $(uptime | sed 's/.*averages: //')"
fi
echo "lock line in stderr log: $(grep -c 'startup: lock' "$T/service.log")  in state/service.log: $(grep -c 'startup: lock' "$T/state/service.log")"
grep -hE "startup:|slow record|restart reconcile" "$T/state/service.log" | cut -c1-260
echo "--- cleanup"
kill "$D" 2>/dev/null; sleep 2; kill -9 "$D" 2>/dev/null
for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
TMUX_TMPDIR=$TMUX_TMPDIR tmux -L leo kill-server 2>/dev/null
echo "artifacts kept in $T"
