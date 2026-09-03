#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version=${VERSION:-v0.3.0-rc.local}
dist=${DIST:-/private/tmp/athena-v03-w0-cross-packages}

platforms=(darwin:arm64 darwin:amd64 linux:arm64 linux:amd64 windows:amd64)
if [[ "${INCLUDE_POSTGRES:-0}" == "1" ]]; then
	for platform in "${platforms[@]}"; do
		os=${platform%:*}
		arch=${platform#*:}
		PLATFORM="${os}-${arch}" POSTGRES_VERSION="${POSTGRES_VERSION:-16.13.0}" OUTPUT_DIR="$dist/postgres" "$root/scripts/package-postgres.sh"
	done
	ASSET_DIR="$dist/postgres" POSTGRES_VERSION="${POSTGRES_VERSION:-16.13.0}" "$root/scripts/verify-postgres-packages.sh"
fi

for platform in "${platforms[@]}"; do
	os=${platform%:*}
	arch=${platform#*:}
	VERSION="$version" TARGET_OS="$os" TARGET_ARCH="$arch" DIST="$dist" "$root/scripts/package-services.sh"
done

VERSION="$version" DIST="$dist" "$root/scripts/build-launchers.sh"
VERSION="$version" ASSET_DIR="$dist/services" "$root/scripts/verify-service-packages.sh"

for platform in "${platforms[@]}"; do
	os=${platform%:*}
	arch=${platform#*:}
	extension=""
	[[ "$os" == windows ]] && extension=".exe"
	launcher="$dist/launchers/athena-launcher_${version}_${os}_${arch}${extension}"
	[[ -f "$launcher" ]] || { echo "missing launcher binary: $launcher" >&2; exit 1; }
done

echo "cross-platform CLI packages passed structural verification in $dist"
