#!/bin/sh
# SolarAgent installer — https://github.com/wetakush/solaragent (open source, MIT)
# usage: curl -fsSL https://raw.githubusercontent.com/wetakush/solaragent/main/install.sh | sh
set -e
REPO="wetakush/solaragent"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)"; exit 1 ;;
esac
case "$os" in linux|darwin|freebsd) ;; *) echo "unsupported OS: $os"; exit 1 ;; esac
asset="solar-${os}-${arch}"
# pick a writable directory that is already on PATH (e.g. /opt/homebrew/bin on macOS), else ~/.local/bin
dir="${SOLAR_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  for d in "$HOME/.local/bin" /opt/homebrew/bin /usr/local/bin "$HOME/bin"; do
    case ":$PATH:" in *":$d:"*) if [ -d "$d" ] && [ -w "$d" ]; then dir="$d"; break; fi ;; esac
  done
  dir="${dir:-$HOME/.local/bin}"
fi
mkdir -p "$dir"
base="https://github.com/${REPO}/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '\033[38;2;255;178;36m☀ SolarAgent\033[0m installing %s → %s\n' "$asset" "$dir"
curl -fsSL "$base/$asset" -o "$tmp/solar"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/solar" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/solar" | cut -d' ' -f1); fi
[ "$want" = "$got" ] || { echo "checksum mismatch"; exit 1; }
chmod +x "$tmp/solar"
mv "$tmp/solar" "$dir/solar"
printf '\033[32m✓\033[0m installed %s\n' "$("$dir/solar" --version)"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    rc="$HOME/.profile"
    case "${SHELL:-}" in */zsh) rc="$HOME/.zshrc" ;; */bash) rc="$HOME/.bashrc" ;; esac
    printf '  %s is not on your PATH — add it with:\n    echo '"'"'export PATH="%s:$PATH"'"'"' >> %s && exec $SHELL\n' "$dir" "$dir" "$rc" ;;
esac
printf '  run \033[1msolar\033[0m to get started (auto-updates are on by default)\n'
