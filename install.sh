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
dir="${SOLAR_INSTALL_DIR:-$HOME/.local/bin}"
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
case ":$PATH:" in *":$dir:"*) ;; *) printf '  add to PATH: export PATH="%s:$PATH"\n' "$dir" ;; esac
printf '  run \033[1msolar\033[0m to get started (auto-updates are on by default)\n'
