#!/bin/sh
# Leo installer — downloads the latest release from GitHub.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/main/install.sh | sh
#
# Options (via environment variables):
#   INSTALL_DIR  — where to place the binary (default: /usr/local/bin)
#   VERSION      — specific version to install (default: latest)

set -eu

REPO="blackpaw-studio/leo"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

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

  echo "Extracting..."
  tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"

  echo "Installing to ${INSTALL_DIR}/leo..."
  if [ -w "$INSTALL_DIR" ]; then
    mv "${tmpdir}/leo" "${INSTALL_DIR}/leo"
  else
    sudo mv "${tmpdir}/leo" "${INSTALL_DIR}/leo"
  fi
  chmod +x "${INSTALL_DIR}/leo"

  echo "leo ${version} installed successfully."
  echo ""
  echo "Run 'leo version' to verify."
}

main
