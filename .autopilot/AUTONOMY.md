# Autonomy — Leo

## Agent decides and logs (DECISIONS.md)
Anything reversible inside the vision: UX details, ordering, implementation approach, naming, polish, bug fixes, test infrastructure, CHANGELOG `[Unreleased]` entries, and small scope cuts or additions that serve the principles.

## Stop list (Evan decides)
- Pushing any branch, merging to the default branch, tagging, releasing, publishing (including the Homebrew tap).
- Anything involving money.
- Irreversible or destructive actions (data deletion, force-push, history rewrites).
- Big creative-direction changes (anything that contradicts or stretches a principle), including anything toward cloud/hosted Leo.
- Removing or hiding a user-facing feature.
- New external services or accounts, or dependencies with licensing or cost.
- Breaking config or API changes (Leo has external users; each needs a migration path and Evan's sign-off).
- `make install`, `leo update`, `leo service restart|stop|start`, or anything else that touches the production daemon, its launchd service, `~/.leo/`, or the production tmux server's sessions. Verify only on the isolated daemon in verify.md.
- `make e2e` outside `env -i … TMUX_TMPDIR=$(mktemp -d)` (it leaks into the live daemon and kills panes otherwise).
- Commenting on, opening, or closing GitHub issues or PRs.
