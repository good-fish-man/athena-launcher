#!/usr/bin/env sh
set -eu

PLATFORM=${PLATFORM:?PLATFORM is required, for example darwin-arm64}
POSTGRES_VERSION=${POSTGRES_VERSION:-16.13.0}
OUTPUT_DIR=${OUTPUT_DIR:-dist/postgres}
MAVEN_BASE=${MAVEN_BASE:-https://repo.maven.apache.org/maven2/io/zonky/test/postgres}

case "$PLATFORM" in
  darwin-arm64) classifier="darwin-arm64v8" ;;
  darwin-amd64) classifier="darwin-amd64" ;;
  linux-arm64) classifier="linux-arm64v8" ;;
  linux-amd64) classifier="linux-amd64" ;;
  windows-amd64) classifier="windows-amd64" ;;
  *) echo "unsupported PostgreSQL platform: $PLATFORM" >&2; exit 1 ;;
esac

artifact="embedded-postgres-binaries-$classifier"
filename="$artifact-$POSTGRES_VERSION.jar"
url="$MAVEN_BASE/$artifact/$POSTGRES_VERSION/$filename"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "downloading PostgreSQL $POSTGRES_VERSION for $PLATFORM"
curl --fail --location --retry 3 --silent --show-error "$url" --output "$work/bundle.jar"
if curl --fail --location --retry 3 --silent --show-error "$url.sha256" --output "$work/bundle.jar.checksum"; then
  expected=$(awk '{print $1}' "$work/bundle.jar.checksum")
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$work/bundle.jar" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$work/bundle.jar" | awk '{print $1}')
  fi
  algorithm="SHA256"
else
  curl --fail --location --retry 3 --silent --show-error "$url.sha512" --output "$work/bundle.jar.checksum"
  expected=$(awk '{print $1}' "$work/bundle.jar.checksum")
  if command -v sha512sum >/dev/null 2>&1; then
    actual=$(sha512sum "$work/bundle.jar" | awk '{print $1}')
  else
    actual=$(shasum -a 512 "$work/bundle.jar" | awk '{print $1}')
  fi
  algorithm="SHA512"
fi
if [ "$actual" != "$expected" ]; then
  echo "Maven artifact $algorithm mismatch: expected $expected, got $actual" >&2
  exit 1
fi

mkdir -p "$work/jar" "$work/unpacked" "$work/package" "$OUTPUT_DIR"
unzip -q "$work/bundle.jar" -d "$work/jar"
archive=$(find "$work/jar" -type f \( -name '*.txz' -o -name '*.tar.xz' \) | head -n 1)
if [ -z "$archive" ]; then
  echo "PostgreSQL archive was not found inside $filename" >&2
  exit 1
fi
tar -xJf "$archive" -C "$work/unpacked"

if [ "$PLATFORM" = "windows-amd64" ]; then
  initdb=$(find "$work/unpacked" -type f -name 'initdb.exe' | head -n 1)
  suffix=".exe"
else
  initdb=$(find "$work/unpacked" -type f -name 'initdb' | head -n 1)
  suffix=""
fi
if [ -z "$initdb" ]; then
  echo "initdb was not found in the PostgreSQL archive" >&2
  exit 1
fi

postgres_root=$(dirname "$(dirname "$initdb")")
cp -R "$postgres_root/." "$work/package/"
for binary in initdb pg_ctl postgres; do
  if [ ! -f "$work/package/bin/$binary$suffix" ]; then
    echo "PostgreSQL package is missing bin/$binary$suffix" >&2
    exit 1
  fi
done

cat > "$work/package/ATHENA-POSTGRES-NOTICE.txt" <<EOF
This package contains PostgreSQL binaries obtained from:
$url

The embedded-postgres-binaries project is distributed under Apache-2.0.
PostgreSQL is distributed under the PostgreSQL License.
EOF

output="$OUTPUT_DIR/postgres_${POSTGRES_VERSION}_${PLATFORM%-*}_${PLATFORM#*-}.tar.gz"
tar -C "$work/package" -czf "$output" .
echo "created $output"
