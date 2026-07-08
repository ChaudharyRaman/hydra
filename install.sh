#!/bin/sh
# Hydra installer — downloads the latest release binary, wires Claude Code
# hooks, and puts hydra on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/ChaudharyRaman/hydra/master/install.sh | sh
#
set -e

REPO="ChaudharyRaman/hydra"
BINDIR="${HOME}/.hydra/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "hydra: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  linux | darwin) ;;
  *) echo "hydra: unsupported OS: $OS (on Windows use WSL, or grab a binary from Releases)" >&2; exit 1 ;;
esac

ASSET="hydra_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

echo "Installing hydra ($OS/$ARCH)..."
mkdir -p "$BINDIR"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "$TMP/hydra.tar.gz"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$TMP/hydra.tar.gz" "$URL"
else
  echo "hydra: need curl or wget" >&2; exit 1
fi

tar -xzf "$TMP/hydra.tar.gz" -C "$TMP"
install -m 0755 "$TMP/hydra" "$BINDIR/hydra"

# Add ~/.hydra/bin to PATH in the user's shell rc (idempotent).
case ":$PATH:" in
  *":$BINDIR:"*) ;;
  *)
    for RC in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
      [ -f "$RC" ] || continue
      grep -q '.hydra/bin' "$RC" && continue
      printf '\n# Hydra\nexport PATH="$PATH:%s"\n' "$BINDIR" >>"$RC"
    done
    ;;
esac

# Wire Claude Code hooks (backs up settings.json first; idempotent).
"$BINDIR/hydra" install || true

echo ""
echo "✓ hydra installed to $BINDIR/hydra"
echo "  Open a new terminal (or run: export PATH=\"\$PATH:$BINDIR\"), then: hydra"
