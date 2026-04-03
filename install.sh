#!/bin/sh
set -e

REPO="scott-pallas/agentswarm"
BINARY="agentswarm-server"
INSTALL_DIR="$HOME/.local/bin"

die() {
  printf "Error: %s\n" "$1" >&2
  exit 1
}

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)      die "Unsupported OS: $OS" ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)             die "Unsupported architecture: $ARCH" ;;
esac

# Get latest release tag
printf "Fetching latest release...\n"
TAG="$(curl -sfL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')" \
  || die "Failed to fetch latest release from GitHub"

[ -z "$TAG" ] && die "Could not determine latest release tag"

printf "Latest release: %s\n" "$TAG"

# Download binary
URL="https://github.com/${REPO}/releases/download/${TAG}/${BINARY}-${OS}-${ARCH}"
printf "Downloading %s-%s-%s...\n" "$BINARY" "$OS" "$ARCH"
mkdir -p "$INSTALL_DIR"
curl -sfL -o "${INSTALL_DIR}/${BINARY}" "$URL" \
  || die "Failed to download ${URL}"

# Make executable
chmod +x "${INSTALL_DIR}/${BINARY}"

printf "Installed %s to %s/%s\n" "$BINARY" "$INSTALL_DIR" "$BINARY"

# Check PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    printf "\nWarning: %s is not in your PATH.\n" "$INSTALL_DIR"
    printf "Add it by appending this to your shell profile:\n"
    printf "  export PATH=\"%s:\$PATH\"\n" "$INSTALL_DIR"
    ;;
esac
