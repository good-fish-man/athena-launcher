#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-0.1.0}
DIST=${DIST:-dist}
MANIFEST_URL=${MANIFEST_URL:-}
OUTPUT="$ROOT/$DIST/launchers"

mkdir -p "$OUTPUT"

build() {
  os=$1
  arch=$2
  extension=$3
  output="$OUTPUT/athena-launcher_${VERSION}_${os}_${arch}${extension}"
  echo "building $os/$arch -> $output"
  (
    cd "$ROOT"
    GOTOOLCHAIN=local CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
      -trimpath \
      -ldflags "-s -w -X main.defaultManifestURL=$MANIFEST_URL" \
      -o "$output" .
  )
}

build darwin arm64 ""
build darwin amd64 ""
build linux amd64 ""
build linux arm64 ""
build windows amd64 ".exe"

echo "launcher binaries are available in $OUTPUT"
