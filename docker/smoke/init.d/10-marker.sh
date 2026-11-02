#!/bin/sh
# Records whether the config existed and the daemon socket did not exist yet
# at the moment init.d ran. Appends one line per container start.
cfg=no; [ -f "$HOME/.leo/leo.yaml" ] && cfg=yes
sock=absent; [ -S "$HOME/.leo/state/leo.sock" ] && sock=present
echo "config=$cfg socket=$sock" >> "$HOME/init-marker"
