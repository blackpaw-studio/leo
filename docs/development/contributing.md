# Contributing

## Build

```bash
make build          # build binary to bin/leo
make install        # install to $GOPATH/bin
```

## Test

```bash
make test           # go test -race -cover ./...
```

Run a single test:

```bash
go test -race -run TestFunctionName ./internal/config/
```

Generate a coverage report:

```bash
go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

## Lint

```bash
make lint           # go vet + staticcheck
```

Install staticcheck if needed:

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
```

## Release

See the [Releasing](releasing.md) guide for the full release pipeline. To test a release build locally:

```bash
make snapshot       # test a release build locally
```

## Documentation

The documentation site uses [MkDocs Material](https://squidfunk.github.io/mkdocs-material/). To preview locally:

```bash
pip install mkdocs-material mkdocs-minify-plugin
mkdocs serve
```

Then open `http://localhost:8000`.

To build the static site:

```bash
mkdocs build --strict
```

The site deploys automatically to GitHub Pages when changes to `docs/` or `mkdocs.yml` are pushed to `main`.

## Project Structure

See the [Architecture](index.md) page for a detailed breakdown of the package layout and design patterns.
