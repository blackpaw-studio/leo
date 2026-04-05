# Installation

## Install Methods

=== "Homebrew"

    The recommended way to install on macOS and Linux:

    ```bash
    brew install blackpaw-studio/tap/leo
    ```

    **Update:**

    ```bash
    brew upgrade leo
    ```

=== "Go"

    Requires Go 1.25+:

    ```bash
    go install github.com/blackpaw-studio/leo@latest
    ```

    The binary is installed to `$GOPATH/bin` (or `$HOME/go/bin` by default). Make sure this directory is in your `PATH`.

=== "From Source"

    ```bash
    git clone https://github.com/blackpaw-studio/leo.git
    cd leo
    make install
    ```

    This builds with version info from git tags and installs to `$GOPATH/bin`.

## Verify Installation

```bash
leo version
```

You should see output like:

```
leo v0.1.0
```

## Next Steps

Before running `leo setup`, make sure you have the [prerequisites](prerequisites.md) in place.
