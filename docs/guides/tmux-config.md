# tmux Config

Leo attaches you to running agents and supervised processes through `tmux`. Every `leo attach`, `leo agent attach`, and `leo process attach` ultimately runs `tmux attach -t leo-<name>` — locally or, for remote hosts, over `ssh -t`. Your `~/.tmux.conf` is the UI you see whenever you're driving an agent interactively.

This guide gives you a recommended starting config, explains what each piece does, and lists the handful of features that matter most for leo workflows.

!!! note "Where should the config live?"
    For remote usage, `~/.tmux.conf` on the **server** is what you're customizing — that's the tmux your `ssh -t … tmux attach` lands in. For local leo installs, it's the laptop's config. If you use both, keep the configs in sync.

## Recommended config

Drop this in `~/.tmux.conf`. It has no plugins, no dependencies, and no assumptions about your color scheme beyond 256-color support.

```tmux
# ========================================
# ~/.tmux.conf — tuned for `leo attach`
# ========================================

# ---- Core ----
set -g default-terminal "tmux-256color"
set -ag terminal-overrides ",xterm-256color:RGB,*256col*:RGB,alacritty:RGB,wezterm:RGB,ghostty:RGB"
set -g escape-time 10
set -g focus-events on
set -g history-limit 100000
set -g mouse on
set -g set-clipboard on              # OSC 52 — copy from remote → local clipboard
setw -g allow-passthrough on         # let apps emit OSC 52 / images through tmux
set -g allow-rename off              # agents can't rename your windows
set -g display-time 2000

# ---- Prefix: Ctrl-a ----
unbind C-b
set -g prefix C-a
bind C-a send-prefix

# ---- Reload ----
bind r source-file ~/.tmux.conf \; display "Reloaded"

# ---- Detach (explicit so nothing shadows the default) ----
bind d detach-client

# ---- Windows/panes start at 1 ----
set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on

# ---- Splits keep current directory ----
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"
bind c new-window    -c "#{pane_current_path}"
unbind '"'
unbind %

# ---- Vim-style pane nav + resize ----
bind h select-pane -L
bind j select-pane -D
bind k select-pane -U
bind l select-pane -R
bind -r H resize-pane -L 5
bind -r J resize-pane -D 5
bind -r K resize-pane -U 5
bind -r L resize-pane -R 5

# ---- Session / window pickers (huge for leo) ----
bind A choose-tree -Zs               # pick a session
bind W choose-tree -Zw               # pick a window across sessions
bind S command-prompt -p "switch to session:" "switch-client -t %%"

# ---- Copy mode: vi keys, y copies to local clipboard ----
setw -g mode-keys vi
bind -T copy-mode-vi v send -X begin-selection
bind -T copy-mode-vi y send -X copy-selection-and-cancel
bind -T copy-mode-vi MouseDragEnd1Pane send -X copy-selection-and-cancel
bind -T copy-mode-vi Escape send -X cancel

# ---- Zoom a pane (handy when reading long agent output) ----
bind z resize-pane -Z

# ---- Status bar ----
set -g status-interval 5
set -g status-position bottom
set -g status-style "bg=colour236,fg=colour245"
set -g status-left-length 50
set -g status-right-length 60
set -g status-left  "#[bg=colour63,fg=colour253,bold] #S #[bg=colour236,fg=colour245] "
set -g status-right "#[fg=colour244]#(whoami)@#h #[fg=colour252] %H:%M "
setw -g window-status-format         " #I:#W "
setw -g window-status-current-format "#[bg=colour238,fg=colour252,bold] #I:#W "
setw -g window-status-activity-style "bg=colour236,fg=colour179"
setw -g monitor-activity on
set  -g visual-activity off
set  -g bell-action none

# ---- Borders ----
set -g pane-border-style "fg=colour238"
set -g pane-active-border-style "fg=colour39"

# ---- Detach cleanly when the session's last pane closes ----
set -g detach-on-destroy on
```

Reload without restarting tmux:

```bash
tmux source-file ~/.tmux.conf
```

## Why these settings, in plain English

### Input and display

- **`default-terminal` + `terminal-overrides`** — advertises 256-color and lets true-color (`RGB`) apps pass through. Without this, Claude Code's syntax highlighting in an agent pane looks washed out.
- **`escape-time 10`** — tmux adds latency to distinguish `Esc` from escape sequences. `0` is tempting but breaks some SSH stacks; `10ms` is invisible.
- **`focus-events on`** — editors and Claude Code detect when the pane gains or loses focus (cursor-blink state, autosave, etc.).
- **`history-limit 100000`** — generous scrollback so long agent transcripts don't fall off the top. Cheap in RAM.
- **`mouse on`** — scroll, click panes to focus, drag borders to resize.

### Clipboard over SSH (the killer feature)

- **`set-clipboard on`** — enables [OSC 52](https://chromium.googlesource.com/apps/libapps/+/master/hterm/doc/ControlSequences.md) so a `y` in copy mode on the **remote** tmux emits an escape sequence that your **local** terminal emulator interprets as "put this on the clipboard."
- **`allow-passthrough on`** — lets apps running inside tmux emit their own OSC 52 / image-protocol / hyperlink sequences without tmux stripping them.

Both are required for clipboard-over-SSH to work. Your local terminal also has to opt in:

| Terminal | Setting |
|---|---|
| iTerm2 | Settings → General → Selection → *"Applications in terminal may access clipboard"* |
| WezTerm | On by default |
| Ghostty | On by default |
| Kitty | `clipboard_control write-clipboard write-primary` |
| Alacritty | On by default |

### Why `allow-rename off` matters for leo

Claude Code and many shells emit escape sequences to set the terminal title. Inside a tmux pane, that turns into a window rename. Leo already named the window `leo-<agent>`; you don't want that overwritten every time Claude updates its status line. `allow-rename off` pins the name so you can tell which agent is which at a glance.

### Why `detach-on-destroy on` and an explicit detach binding

Two small things that are annoying to get wrong:

- **`set -g detach-on-destroy on`** — when the session's last pane exits (e.g. you `Ctrl-C` out of Claude Code), tmux detaches cleanly and drops you back at your login shell. With `off`, tmux instead switches you to some other running session, which is jarring if you just wanted to end the thing you were in. `on` is also the tmux default; the line is in the config so the intent is explicit and doesn't get confused with the alternatives (`off`, `previous`, `next`).
- **`bind d detach-client`** — detach-on-prefix-d is tmux's default, but bindings from plugins, shared configs, or muscle-memory tweaks can shadow it. Pinning it explicitly guarantees `prefix + d` always detaches, no matter what else is in your config.

If you'd rather hop between running leo agents when one exits — nice if you routinely have several active at once — swap `on` for `off`.

### Activity monitoring

- **`monitor-activity on`** — tmux watches every window for output.
- **`window-status-activity-style`** — when a background window (a different agent) produces output, its tab changes color. Passive signal that something wants attention, without forcing you to cycle through panes.
- **`visual-activity off`** and **`bell-action none`** — no disruptive popups or beeps when the color change is enough.

### Session picker

`prefix + A` pops a fuzzy picker of every tmux session — great when leo has multiple agents and processes running side by side. `prefix + W` does the same but lists every window across sessions. Both are faster than `tmux ls && tmux attach -t <name>`.

### Pane zoom

`prefix + z` toggles the current pane fullscreen. Handy when an agent produces a long block of output and you want to read it without the status bar or sibling panes in the way. Press it again to zoom out.

### Copy mode

- `prefix + [` enters copy mode.
- `v` starts selection, `y` copies (and — thanks to OSC 52 — the selection lands on your local clipboard, even over SSH).
- Mouse drag works too; releasing the button copies.
- `q` exits.

## Cheat sheet

| Keys | Action |
|---|---|
| `prefix + A` | Pick a session |
| `prefix + W` | Pick a window across sessions |
| `prefix + S` | Switch to session by name |
| `prefix + z` | Zoom/unzoom current pane |
| `prefix + [` | Enter scrollback / copy mode (`q` to exit) |
| `prefix + d` | Detach (leaves leo running) |
| `prefix + r` | Reload `~/.tmux.conf` |
| `prefix + \|` / `prefix + -` | Split pane horizontally / vertically |
| `prefix + h/j/k/l` | Focus pane in direction |
| `prefix + H/J/K/L` | Resize pane in direction |

`prefix` is `Ctrl-a` with the config above.

## FAQ

### Should I install tmux-resurrect or tmux-continuum?

Not for leo. Those plugins snapshot session state to disk and restore it on tmux restart. Leo owns its session lifecycle — it names sessions, spawns and stops them — so resurrect's restore would race against leo's own process manager and leave orphan panes. If you want session persistence, use `leo service start --daemon` (see [Background Mode](background-mode.md)) so leo itself restarts on reboot.

### Do I need this config on my laptop or on the server?

For `leo attach` against a remote host (`ssh -t host tmux attach`), the tmux config that matters is the **server's** `~/.tmux.conf`. The local terminal emulator still handles things like OSC 52 clipboard writes — which is why both sides need to agree.

For a local leo install where you run `leo service start` on the same machine you attach from, it's just your laptop's config.

### Why Ctrl-a instead of Ctrl-b?

Ctrl-b collides with `readline`'s "move cursor back one character" and is awkward to hit. Ctrl-a is the traditional screen/tmux binding and sits under your pinky. Swap it for anything you like — `C-space` and `` C-` `` are also popular.

### My terminal says `tmux-256color: unknown terminal type`

The server is missing the terminfo entry. Either install it (`ncurses-term` on Debian/Ubuntu, usually already there on macOS) or fall back to `screen-256color`:

```tmux
set -g default-terminal "screen-256color"
```

The `terminal-overrides` line in the recommended config still re-enables true color.

### Can I use hex colors instead of the 256-color palette?

Yes. Because `terminal-overrides` advertises `RGB`, tmux accepts hex in any style string:

```tmux
set -g status-style "bg=#2e1a47,fg=#d0c9d8"
```

### How do I browse the 256-color palette to pick different status bar colors?

Run this inside your terminal:

```bash
for i in {0..255}; do
  printf "\e[48;5;%dm %3d \e[0m" "$i"
  (( (i+1) % 16 == 0 )) && echo
done
```

Useful ranges: grays are `232–255`, blues cluster around `17–39`, purples `53–99`, greens `22–82`.

## See also

- [Remote CLI](remote-cli.md) — how `leo attach` reaches a remote host over SSH
- [Background Mode](background-mode.md) — keeping leo's tmux sessions alive across reboots
- [Agents guide](agents.md) — spawning the agents you'll attach to
