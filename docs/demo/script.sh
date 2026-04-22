#!/usr/bin/env bash
# Typewriter demo recorded by asciinema. Assumes:
#   - `leo service` is already running (entrypoint.sh starts it)
#   - three "coding" agents have been spawned as preamble
#
# Edits here re-render the GIF on the next `make demo`.

set -u

PROMPT=$'\033[1;32m$\033[0m '
CYAN=$'\033[36m'
DIM=$'\033[2m'
BOLD=$'\033[1m'
RESET=$'\033[0m'

type_cmd() {
  local cmd="$1"
  printf '%s' "$PROMPT"
  local i
  for (( i=0; i<${#cmd}; i++ )); do
    printf '%s' "${cmd:$i:1}"
    sleep 0.035
  done
  printf '\n'
  sleep 0.35
}

say() {
  printf '%s%s%s\n' "$DIM" "$1" "$RESET"
  sleep "${2:-1.2}"
}

pause() { sleep "$1"; }

clear
sleep 0.5
printf '%sleo%s — persistent Claude Code supervisor\n' "$BOLD" "$RESET"
printf '%sagents · scheduled tasks · one daemon%s\n\n' "$CYAN" "$RESET"
sleep 1.6

type_cmd "leo"
leo 2>&1 | sed -n '1,30p'
pause 3.2

echo
say "# one command shows what's alive:"
type_cmd "leo status"
leo status 2>&1
pause 3.0

echo
say "# ephemeral agents spawn from templates, each in its own tmux session:"
type_cmd "leo agent list"
leo agent list 2>&1
pause 2.8

echo
say "# spawn a fresh one and attach to its tmux session:"
type_cmd "leo agent spawn coding demo-showcase"
leo agent spawn coding demo-showcase 2>&1
pause 1.4

# Leo spawns tmux sessions at a hard-coded 200x50. Resize the demo session
# down to our recording width so the Claude REPL fills the GIF cleanly.
tmux -L leo resize-window -t leo-coding-demo-showcase -x 110 -y 24 2>/dev/null || true
sleep 5

type_cmd "leo agent attach leo-coding-demo-showcase"
# Real tmux attach requires an interactive TTY; we emit the live pane
# contents directly so the recording shows the same picture. Leo uses
# its own tmux server name (`-L leo`), not the default socket.
tmux -L leo capture-pane -t leo-coding-demo-showcase -e -p 2>/dev/null \
  | awk '{lines[NR]=$0} /[^[:space:]]/ {last=NR} END {for(i=1;i<=last;i++) print lines[i]}'
pause 4.5
printf '\n\033[2m[detached]\033[0m\n'
pause 0.8

echo
type_cmd "leo agent stop leo-coding-demo-showcase"
leo agent stop leo-coding-demo-showcase 2>&1
pause 1.4

echo
printf '%sgithub.com/blackpaw-studio/leo%s\n' "$CYAN" "$RESET"
sleep 2.5
