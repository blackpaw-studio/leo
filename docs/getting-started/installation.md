# Installation

## Install Methods

=== "Homebrew (recommended)"

    macOS and Linux:

    ```bash
    brew install blackpaw-studio/tap/leo
    ```

    **Upgrade:**

    ```bash
    brew upgrade blackpaw-studio/tap/leo && leo service restart
    ```

    `leo update` detects the Homebrew install and prints these commands instead of trying to replace the Homebrew-managed binary.

=== "Install Script"

    Short-URL installer served from `leo.blackpaw.studio` (redirects to the version-pinned script in the latest release):

    ```bash
    curl -fsSL leo.blackpaw.studio/install | sh
    ```

    The installer downloads the release tarball for your platform, fetches the release `checksums.txt`, and verifies the SHA-256 hash before installing. If `cosign` is present on your PATH it also verifies the Sigstore keyless signature on `checksums.txt` against the GitHub OIDC issuer and the release tag.

    **Options:**

    ```bash
    # Custom install directory
    INSTALL_DIR=~/.local/bin curl -fsSL leo.blackpaw.studio/install | sh

    # Pin a specific version
    VERSION=v0.3.2 curl -fsSL leo.blackpaw.studio/install | sh
    ```

    **Upgrade:** `leo update` replaces the binary in place after the same checksum + cosign verification.

=== "Go"

    Requires Go 1.25+:

    ```bash
    go install github.com/blackpaw-studio/leo/cmd/leo@latest
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

    Download the archive for your platform from the [Releases page](https://github.com/blackpaw-studio/leo/releases/latest), extract it, and move the `leo` binary to a directory in your `PATH`. Verify the archive against `checksums.txt` from the same release before running it.

## Verify Installation

```bash
leo version
```

You should see output like:

```
leo v0.3.2
```

## Next Steps

Before running `leo setup`, make sure you have the [prerequisites](prerequisites.md) in place.
