# Contributing to Leo

Thanks for your interest in contributing! This file covers the essentials.
For deeper docs on architecture, release process, and documentation site
tooling, see [docs/development/](docs/development/).

## Quick start

```bash
git clone https://github.com/blackpaw-studio/leo.git
cd leo
make build
make test
```

Requirements: Go 1.25+, `tmux`, and an authenticated `claude` CLI for
anything that exercises the supervisor.

## Before opening a PR

1. **Open an issue first** for non-trivial changes. This gives us a chance
   to agree on the approach before you invest the time.
2. Run the full local check:
   ```bash
   make test
   make lint
   ```
3. Keep PRs focused. One logical change per PR — split drive-by fixes into
   their own commits or PRs.
4. Update the [CHANGELOG](CHANGELOG.md) under an `Unreleased` heading for
   user-visible changes.
5. If you add a new CLI command or config field, update `docs/`.

## Commit messages

Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`,
`chore:`, `perf:`, `ci:`). The PR title becomes the squash-merge commit
message, so keep it in this format.

## Testing

We enforce 45% minimum coverage on the `test` job and run the `-race`
detector. See [docs/development/contributing.md](docs/development/contributing.md#test)
for how to generate an HTML coverage report.

New code should come with tests. Bug fixes should come with a regression
test that fails before the fix and passes after.

## Security issues

**Please do not open public issues for security vulnerabilities.** See
[SECURITY.md](SECURITY.md) for how to report them privately.
