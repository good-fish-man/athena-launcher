#!/usr/bin/env bash
set -euo pipefail

version=${VERSION:-v0.3.0-rc.local}
dist=${DIST:?DIST must contain services/ and launchers/}
postgres_dist=${POSTGRES_DIST:?POSTGRES_DIST must contain all embedded PostgreSQL archives}
evidence_dir=${ATHENA_W0_EVIDENCE_DIR:-$dist/evidence}
run_id=${ATHENA_W0_RUN_ID:-v03-w0-packages-$(date -u +%Y%m%dT%H%M%SZ)}
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
assets_file="$evidence_dir/package-assets.ndjson"
evidence_file="$evidence_dir/cross-package-evidence.json"

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
mkdir -p "$evidence_dir"
: >"$assets_file"

VERSION="$version" ASSET_DIR="$dist/services" "$root/scripts/verify-service-packages.sh"
POSTGRES_VERSION="${POSTGRES_VERSION:-16.13.0}" ASSET_DIR="$postgres_dist" "$root/scripts/verify-postgres-packages.sh"

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

record_asset() {
	local kind=$1
	local path=$2
	local digest size
	digest=$(sha256_file "$path")
	if stat -f %z "$path" >/dev/null 2>&1; then
		size=$(stat -f %z "$path")
	else
		size=$(stat -c %s "$path")
	fi
	jq -n -c --arg kind "$kind" --arg path "$path" --arg sha256 "$digest" --argjson size_bytes "$size" \
		'{kind:$kind,path:$path,sha256:$sha256,size_bytes:$size_bytes}' >>"$assets_file"
}

while IFS= read -r path; do record_asset service_archive "$path"; done < <(find "$dist/services" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | sort)
while IFS= read -r path; do record_asset launcher_cli "$path"; done < <(find "$dist/launchers" -maxdepth 1 -type f | sort)
while IFS= read -r path; do record_asset postgres_archive "$path"; done < <(find "$postgres_dist" -maxdepth 1 -type f -name '*.tar.gz' | sort)

asset_count=$(wc -l <"$assets_file" | tr -d ' ')
if [[ "$asset_count" -ne 20 ]]; then
	echo "cross-package evidence contains $asset_count assets; want 20" >&2
	exit 1
fi

jq -s \
	--arg schema "athena.internal.v0.3.w0.cross-package-evidence.v1" \
	--arg run_id "$run_id" \
	--arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	'{
		schema:$schema,
		run_id:$run_id,
		scope:"CROSS_COMPILED_UNSIGNED_STRUCTURE",
		status:"PASS",
		generated_at:$generated_at,
		platforms:["darwin-arm64","darwin-amd64","linux-arm64","linux-amd64","windows-amd64"],
		assets:.,
		assertions:[
			{id:"service_archive_manifest_paths",status:"PASS"},
			{id:"launcher_cli_cross_compile",status:"PASS"},
			{id:"embedded_postgres_server_binaries",status:"PASS"}
		],
		external_gates:[
			{id:"macos_signed_notarized_installer_smoke",status:"EXTERNAL_REQUIRED"},
			{id:"windows_authenticode_installer_smoke",status:"EXTERNAL_REQUIRED"},
			{id:"linux_signed_appimage_smoke",status:"EXTERNAL_REQUIRED"}
		]
	}' "$assets_file" >"$evidence_file"

echo "wrote $evidence_file"
