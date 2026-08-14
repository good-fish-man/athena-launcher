#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE=$(CDPATH= cd -- "$ROOT/.." && pwd)
VERSION=${VERSION:-1.0.0}
TARGET_OS=${TARGET_OS:-$(go env GOOS)}
TARGET_ARCH=${TARGET_ARCH:-$(go env GOARCH)}
DIST=${DIST:-dist}
OUTPUT="$ROOT/$DIST/services"
STAGING="$ROOT/$DIST/staging-$TARGET_OS-$TARGET_ARCH"
EXE=""

if [ "$TARGET_OS" = "windows" ]; then
  EXE=".exe"
fi

rm -rf "$STAGING"
mkdir -p "$STAGING/agent-runtime" "$STAGING/agent-runtime-client" "$OUTPUT"

echo "building agent-runtime for $TARGET_OS/$TARGET_ARCH"
(
  cd "$WORKSPACE/agent-runtime"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -trimpath -ldflags "-s -w" -o "$STAGING/agent-runtime/agent-runtime$EXE" ./cmd/server
)
cp -R "$WORKSPACE/agent-runtime/skills" "$STAGING/agent-runtime/skills"

echo "building agent-runtime-client for $TARGET_OS/$TARGET_ARCH"
(
  cd "$WORKSPACE/agent-runtime-client"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -trimpath -ldflags "-s -w" -o "$STAGING/agent-runtime-client/agent-runtime-client$EXE" .
)

runtime_archive="$OUTPUT/agent-runtime_${VERSION}_${TARGET_OS}_${TARGET_ARCH}.tar.gz"
client_archive="$OUTPUT/agent-runtime-client_${VERSION}_${TARGET_OS}_${TARGET_ARCH}.tar.gz"
tar -C "$STAGING/agent-runtime" -czf "$runtime_archive" .
tar -C "$STAGING/agent-runtime-client" -czf "$client_archive" .

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

checksum "$runtime_archive"
checksum "$client_archive"
echo "service packages are available in $OUTPUT"
