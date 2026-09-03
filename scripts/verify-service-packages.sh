#!/usr/bin/env bash
set -euo pipefail

version=${VERSION:-v1.0.0}
asset_dir=${ASSET_DIR:-}
if [[ -z "$asset_dir" ]]; then
	echo "ASSET_DIR is required" >&2
	exit 2
fi

platforms=(darwin_arm64 darwin_amd64 linux_arm64 linux_amd64 windows_amd64)
for platform in "${platforms[@]}"; do
	os=${platform%_*}
	arch=${platform#*_}
	for service in agent-runtime agent-runtime-client; do
		name="${service}_${version}_${os}_${arch}"
		executable="$service"
		if [[ "$os" == windows ]]; then
			archive="$asset_dir/$name.zip"
			executable="$executable.exe"
			[[ -f "$archive" ]] || { echo "missing package: $archive" >&2; exit 1; }
			unzip -Z1 "$archive" | grep -Fx "$name/$executable" >/dev/null || {
				echo "package $archive is missing $name/$executable" >&2
				exit 1
			}
		else
			archive="$asset_dir/$name.tar.gz"
			[[ -f "$archive" ]] || { echo "missing package: $archive" >&2; exit 1; }
			tar -tzf "$archive" | sed 's#^\./##' | grep -Fx "$name/$executable" >/dev/null || {
				echo "package $archive is missing $name/$executable" >&2
				exit 1
			}
		fi
	done
done

echo "all service archives match release-manifest executable paths"
