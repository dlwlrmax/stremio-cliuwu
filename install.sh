#!/usr/bin/env bash
#
# Build stremio-cliuwu and install it where you can run it from anywhere.
#
#   ./install.sh              build + install to ~/.local/bin
#   ./install.sh --uninstall  remove the installed binary
#   PREFIX=/usr/local ./install.sh   install elsewhere (needs write access)
#
# Requires: go, mpv (mpv only needed at runtime).

set -euo pipefail

BINARY="stremio-cliuwu"
INSTALL_DIR="${PREFIX:-$HOME/.local/bin}"

cd "$(dirname "$0")"

uninstall() {
  rm -f "${INSTALL_DIR}/${BINARY}"
  echo "  ✓ removed ${INSTALL_DIR}/${BINARY}"
}

if [[ "${1:-}" == "--uninstall" ]]; then
  uninstall
  exit 0
fi

command -v go >/dev/null 2>&1 || {
  echo "error: go not found on PATH — install Go first (https://go.dev/dl/)" >&2
  exit 1
}

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
VERSION="${VERSION#v}"

echo "  ⟳ building ${BINARY} (${VERSION})"
go build -ldflags="-s -w -X main.version=${VERSION}" -o "${BINARY}" .

mkdir -p "${INSTALL_DIR}"
install -m755 "${BINARY}" "${INSTALL_DIR}/${BINARY}"
echo "  ✓ installed → ${INSTALL_DIR}/${BINARY}"

command -v mpv >/dev/null 2>&1 || {
  echo "  ! mpv not found — the app needs it at runtime (https://mpv.io/installation/)" >&2
}

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo "  ! ${INSTALL_DIR} is not on your PATH. Add this line to ~/.bashrc (or ~/.zshrc):"
    echo "      export PATH=\"\$HOME/.local/bin:\$PATH\""
    ;;
esac
