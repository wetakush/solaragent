#!/usr/bin/env bash
# Build SolarAgent for all platforms and publish a GitHub release (no CI needed).
# usage: scripts/release.sh v1.2.0     (creates & pushes the tag if missing)
set -euo pipefail
TAG="${1:?usage: scripts/release.sh vX.Y.Z}"
[[ "$TAG" == v* ]] || TAG="v$TAG"
VERSION="${TAG#v}"
cd "$(dirname "$0")/.."

[[ -z "$(git status --porcelain)" ]] || { echo "working tree is dirty — commit first"; exit 1; }
go vet ./... && go test ./...

if ! git rev-parse "$TAG" >/dev/null 2>&1; then git tag "$TAG"; fi
git push origin HEAD "$TAG"

rm -rf dist && mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/solar-${os}-${arch}"; [[ "$os" == windows ]] && out="${out}.exe"
  echo "☀ building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X github.com/wetakush/solaragent/internal/app.Version=${VERSION}" -o "$out" .
done
(cd dist && sha256sum solar-* > checksums.txt)

if gh release view "$TAG" >/dev/null 2>&1; then
  gh release upload "$TAG" dist/* --clobber
else
  gh release create "$TAG" dist/* --title "SolarAgent $TAG" --generate-notes
fi
echo "✓ released $TAG"
