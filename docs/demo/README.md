# Demo recording

Containerised, reproducible asciinema recording of a typical `leo` session.
Produces `out/leo-demo.cast` and `out/leo-demo.gif` from a controlled config —
no sanitisation, no host state leakage.

## Running

```
make demo
```

or directly:

```
bash docs/demo/record.sh
```

Requires Docker. First build takes a few minutes (pulls Debian, Node, the
Claude Code CLI, `agg`). Subsequent runs with `SKIP_BUILD=1` reuse the image.

## Layout

| File               | Purpose                                                    |
|--------------------|------------------------------------------------------------|
| `Dockerfile`       | Multi-stage image: builder compiles `leo`, runtime adds `tmux`, `asciinema`, `agg`, Claude Code |
| `leo.yaml`         | Fictional demo config (2 processes, 4 tasks, 1 template)   |
| `prompts/`         | Placeholder prompt files referenced by the tasks           |
| `entrypoint.sh`    | Container entry: boots daemon, preloads agents, records    |
| `script.sh`        | Typewriter demo recorded by asciinema (edit to change pacing / commands) |
| `record.sh`        | Host-side driver invoked by `make demo`                    |
| `out/`             | Generated `.cast` and `.gif` (gitignored)                  |

## Tweaks

- **Pacing** — `sleep` calls in `script.sh`
- **Terminal size** — `WINDOW_SIZE` env var (default `130x36`)
- **Font size** — `FONT_SIZE` env var (default `13`)
- **Config surface** — edit `leo.yaml`; what shows up in `leo status`,
  `leo task list`, etc. comes straight from there

## Why containerised

- Runs against a generated `leo.yaml` with fictional process and task names
  — no sanitisation regexes needed
- Reproducible: anyone with Docker can regenerate the GIF after a UI change
- Isolated: doesn't touch the host's `~/.leo` or `~/.claude`
