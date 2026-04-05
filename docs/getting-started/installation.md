# Installation

## Install Methods

=== "Install Script"

    The quickest way to install on macOS and Linux:

    ```bash
    curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
    ```

    This downloads the latest release binary for your platform and installs it to `/usr/local/bin`.

    **Options:**

    ```bash
    # Install to a custom directory
    INSTALL_DIR=~/.local/bin curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh

    # Install a specific version
    VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
    ```

    **Update:** Re-run the install script to get the latest version.

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

=== "Manual Download"

    Download the archive for your platform from the [Releases page](https://github.com/blackpaw-studio/leo/releases/latest), extract it, and move the `leo` binary to a directory in your `PATH`.

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
