#!/usr/bin/env bash
set -euo pipefail

version=${POSTGRES_VERSION:-16.13.0}
asset_dir=${ASSET_DIR:-}
if [[ -z "$asset_dir" ]]; then
	echo "ASSET_DIR is required" >&2
	exit 2
fi

platforms=(darwin_arm64 darwin_amd64 linux_arm64 linux_amd64 windows_amd64)
for platform in "${platforms[@]}"; do
	os=${platform%_*}
	arch=${platform#*_}
	archive="$asset_dir/postgres_${version}_${os}_${arch}.tar.gz"
	[[ -f "$archive" ]] || { echo "missing PostgreSQL package: $archive" >&2; exit 1; }
	suffix=""
	[[ "$os" == windows ]] && suffix=".exe"
	entries=$(tar -tzf "$archive" | sed 's#^\./##')
	for binary in initdb pg_ctl postgres; do
		grep -Fx "bin/${binary}${suffix}" <<<"$entries" >/dev/null || {
			echo "package $archive is missing bin/${binary}${suffix}" >&2
			exit 1
		}
	done
	grep -Fx "ATHENA-POSTGRES-NOTICE.txt" <<<"$entries" >/dev/null || {
		echo "package $archive is missing ATHENA-POSTGRES-NOTICE.txt" >&2
		exit 1
	}
done

echo "all embedded PostgreSQL archives contain the required server lifecycle binaries"
