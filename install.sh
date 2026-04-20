#!/bin/sh
# Leo installer — downloads the latest release from GitHub.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
#
# Options (via environment variables):
#   INSTALL_DIR  — where to place the binary (default: ~/.local/bin)
#   VERSION      — specific version to install (default: latest)

set -eu

REPO="blackpaw-studio/leo"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS and architecture
detect_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    darwin) os="darwin" ;;
    linux)  os="linux" ;;
    *)
      echo "Error: unsupported OS: $os" >&2
      exit 1
      ;;
  esac

  case "$arch" in
    x86_64|amd64)  arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)
      echo "Error: unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac

  echo "${os}_${arch}"
}

# Resolve version tag
resolve_version() {
  if [ -n "${VERSION:-}" ]; then
    echo "$VERSION"
    return
  fi

  version="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

  if [ -z "$version" ]; then
    echo "Error: could not determine latest version" >&2
    exit 1
  fi

  echo "$version"
}

# Find the user's shell profile file
detect_shell_profile() {
  shell_name="$(basename "${SHELL:-}")"

  case "$shell_name" in
    zsh)
      echo "$HOME/.zshrc"
      ;;
    bash)
      # Prefer .bashrc on Linux, .bash_profile on macOS
      if [ -f "$HOME/.bashrc" ]; then
        echo "$HOME/.bashrc"
      elif [ -f "$HOME/.bash_profile" ]; then
        echo "$HOME/.bash_profile"
      else
        echo "$HOME/.bashrc"
      fi
      ;;
    fish)
      echo "$HOME/.config/fish/config.fish"
      ;;
    *)
      # Unknown shell — return empty, caller will print manual instructions
      echo ""
      ;;
  esac
}

main() {
  platform="$(detect_platform)"
  version="$(resolve_version)"
  version_num="${version#v}"

  archive="leo_${version_num}_${platform}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${version}/${archive}"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  echo "Downloading leo ${version} for ${platform}..."
  curl -fsSL "$url" -o "${tmpdir}/${archive}"

  echo "Verifying checksum..."
  checksums_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"
  if ! curl -fsSL "$checksums_url" -o "${tmpdir}/checksums.txt"; then
    echo "Error: could not fetch checksums.txt from ${checksums_url}" >&2
    echo "Refusing to install an unverified binary. Re-run after the release has fully published," >&2
    echo "or use a tagged release: VERSION=vX.Y.Z sh install.sh" >&2
    exit 1
  fi

  expected="$(grep -E "[[:space:]]${archive}\$" "${tmpdir}/checksums.txt" | awk '{print $1}')"
  if [ -z "$expected" ]; then
    echo "Error: ${archive} not listed in checksums.txt" >&2
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')"
  else
    echo "Error: no sha256 tool available (sha256sum or shasum)" >&2
    exit 1
  fi

  if [ "$expected" != "$actual" ]; then
    echo "Error: checksum mismatch for ${archive}" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    exit 1
  fi

  echo "Extracting..."
  tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"

  mkdir -p "$INSTALL_DIR"
  mv "${tmpdir}/leo" "${INSTALL_DIR}/leo"
  chmod +x "${INSTALL_DIR}/leo"

  echo "leo ${version} installed to ${INSTALL_DIR}/leo"

  # Add INSTALL_DIR to PATH if needed
  case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      profile="$(detect_shell_profile)"

      if [ -n "$profile" ]; then
        if echo "$profile" | grep -q "fish"; then
          echo "fish_add_path ${INSTALL_DIR}" >> "$profile"
        else
          echo "export PATH=\"${INSTALL_DIR}:\$PATH\"" >> "$profile"
        fi
        echo "Added ${INSTALL_DIR} to PATH in ${profile}"
      else
        echo ""
        echo "Warning: ${INSTALL_DIR} is not in your PATH."
        echo "Add this line to your shell profile:"
        echo ""
        echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      fi
      ;;
  esac

  echo ""
  echo "To get started, open a new terminal and run:"
  echo ""
  echo "  leo setup"
}

main
