#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORKSPACE=$(CDPATH= cd -- "$ROOT/.." && pwd)
VERSION=${VERSION:-v1.0.0}
TARGET_OS=${TARGET_OS:-$(go env GOOS)}
TARGET_ARCH=${TARGET_ARCH:-$(go env GOARCH)}
DIST=${DIST:-dist}
case "$DIST" in
  /*) DIST_ROOT=$DIST ;;
  *) DIST_ROOT="$ROOT/$DIST" ;;
esac
OUTPUT="$DIST_ROOT/services"
STAGING="$DIST_ROOT/staging-$TARGET_OS-$TARGET_ARCH"
EXE=""

if [ "$TARGET_OS" = "windows" ]; then
  EXE=".exe"
fi

runtime_name="agent-runtime_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
client_name="agent-runtime-client_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
runtime_stage="$STAGING/$runtime_name"
client_stage="$STAGING/$client_name"

rm -rf "$STAGING"
mkdir -p "$runtime_stage" "$client_stage" "$OUTPUT"

echo "building agent-runtime for $TARGET_OS/$TARGET_ARCH"
(
  cd "$WORKSPACE/agent-runtime"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -trimpath -ldflags "-s -w" -o "$runtime_stage/agent-runtime$EXE" ./cmd/server
)
cp "$WORKSPACE/agent-runtime/README.md" "$WORKSPACE/agent-runtime/README.zh-CN.md" \
  "$WORKSPACE/agent-runtime/LICENSE" "$WORKSPACE/agent-runtime/NOTICE" \
  "$WORKSPACE/agent-runtime/THIRD_PARTY_NOTICES.md" "$WORKSPACE/agent-runtime/config.yaml" "$runtime_stage/"
mkdir -p "$runtime_stage/skills"
for skill in "$WORKSPACE"/agent-runtime/skills/*; do
  [ "$(basename "$skill")" = "pptx" ] && continue
  cp -R "$skill" "$runtime_stage/skills/"
done

echo "building agent-runtime-client for $TARGET_OS/$TARGET_ARCH"
(
  cd "$WORKSPACE/agent-runtime-client"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -trimpath -ldflags "-s -w" -o "$client_stage/agent-runtime-client$EXE" .
)
cp "$WORKSPACE/agent-runtime-client/README.md" "$WORKSPACE/agent-runtime-client/README.zh-CN.md" \
  "$WORKSPACE/agent-runtime-client/LICENSE" "$WORKSPACE/agent-runtime-client/NOTICE" \
  "$WORKSPACE/agent-runtime-client/THIRD_PARTY_NOTICES.md" "$client_stage/"
cp -R "$WORKSPACE/agent-runtime-client/manifest" "$client_stage/manifest"

if [ "$TARGET_OS" = "windows" ]; then
  runtime_archive="$OUTPUT/$runtime_name.zip"
  client_archive="$OUTPUT/$client_name.zip"
  (cd "$STAGING" && zip -qr "$runtime_archive" "$runtime_name")
  (cd "$STAGING" && zip -qr "$client_archive" "$client_name")
else
  runtime_archive="$OUTPUT/$runtime_name.tar.gz"
  client_archive="$OUTPUT/$client_name.tar.gz"
  tar -C "$STAGING" -czf "$runtime_archive" "$runtime_name"
  tar -C "$STAGING" -czf "$client_archive" "$client_name"
fi

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
