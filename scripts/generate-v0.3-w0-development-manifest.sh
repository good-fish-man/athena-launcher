#!/usr/bin/env bash
set -euo pipefail

version=${VERSION:-0.3.0-rc.local}
platform=${PLATFORM:-"$(go env GOOS)-$(go env GOARCH)"}
service_dir=${SERVICE_DIR:-/private/tmp/athena-v03-w0-cross-packages-current/services}
postgres_dir=${POSTGRES_DIR:-/private/tmp/athena-v03-w0-postgres-cross}
browser_bin=${BROWSER_BIN:-$HOME/.athena/browser/0.33.1/agent-browser}
output=${OUTPUT:-/private/tmp/athena-v03-w0-packaged-e2e/release-manifest.json}
postgres_version=${POSTGRES_VERSION:-16.13.0}
browser_version=${BROWSER_VERSION:-0.33.1}

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
case "$platform" in
	darwin-arm64|darwin-amd64|linux-arm64|linux-amd64|windows-amd64) ;;
	*) echo "unsupported platform: $platform" >&2; exit 2 ;;
esac

os=${platform%-*}
arch=${platform#*-}
archive_extension=tar.gz
executable_extension=
if [[ "$os" == windows ]]; then
	archive_extension=zip
	executable_extension=.exe
fi

runtime_name="agent-runtime_v${version}_${os}_${arch}"
client_name="agent-runtime-client_v${version}_${os}_${arch}"
runtime_archive="$service_dir/${runtime_name}.${archive_extension}"
client_archive="$service_dir/${client_name}.${archive_extension}"
postgres_archive="$postgres_dir/postgres_${postgres_version}_${os}_${arch}.tar.gz"

for file in "$runtime_archive" "$client_archive" "$postgres_archive" "$browser_bin"; do
	[[ -f "$file" ]] || { echo "required development asset is missing: $file" >&2; exit 1; }
	[[ "$file" == /* ]] || { echo "development asset must use an absolute path: $file" >&2; exit 1; }
done

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

file_size() { wc -c <"$1" | tr -d '[:space:]'; }
file_url() { printf 'file://%s' "$1"; }

mkdir -p "$(dirname "$output")"
zero_hash=$(printf '0%.0s' {1..64})
issued_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
if date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
	expires_at=$(date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%SZ')
else
	expires_at=$(date -u -v+1d '+%Y-%m-%dT%H:%M:%SZ')
fi

jq -n \
	--arg version "$version" \
	--arg platform "$platform" \
	--arg issued_at "$issued_at" \
	--arg expires_at "$expires_at" \
	--arg zero_hash "$zero_hash" \
	--arg postgres_version "$postgres_version" \
	--arg postgres_url "$(file_url "$postgres_archive")" \
	--arg postgres_sha "$(sha256_file "$postgres_archive")" \
	--argjson postgres_size "$(file_size "$postgres_archive")" \
	--arg browser_version "$browser_version" \
	--arg browser_url "$(file_url "$browser_bin")" \
	--arg browser_sha "$(sha256_file "$browser_bin")" \
	--argjson browser_size "$(file_size "$browser_bin")" \
	--arg runtime_url "$(file_url "$runtime_archive")" \
	--arg runtime_sha "$(sha256_file "$runtime_archive")" \
	--argjson runtime_size "$(file_size "$runtime_archive")" \
	--arg runtime_executable "$runtime_name/agent-runtime${executable_extension}" \
	--arg client_url "$(file_url "$client_archive")" \
	--arg client_sha "$(sha256_file "$client_archive")" \
	--argjson client_size "$(file_size "$client_archive")" \
	--arg client_executable "$client_name/agent-runtime-client${executable_extension}" \
	'{
		schema:"athena.release-manifest.v1",
		release_id:("athena-v" + $version + "-development"),
		version:$version,
		protocol_version:"1.0.0",
		minimum_from_version:"0.0.0",
		development:true,
		sbom_url:"https://development.invalid/release-sbom.spdx.json",
		sbom_sha256:$zero_hash,
		signature:{},
		issued_at:$issued_at,
		expires_at:$expires_at,
		database:{
			version:$postgres_version,
			bin_dir:"bin",
			artifacts:{($platform):{
				url:$postgres_url,sha256:$postgres_sha,size_bytes:$postgres_size,
				sbom_sha256:$zero_hash,signature:{},code_signing:"DEVELOPMENT",format:"tar.gz",executable:""
			}}
		},
		browser:{
			version:$browser_version,
			artifacts:{($platform):{
				url:$browser_url,sha256:$browser_sha,size_bytes:$browser_size,
				sbom_sha256:$zero_hash,signature:{},code_signing:"DEVELOPMENT",format:"raw",executable:(if ($platform|startswith("windows-")) then "agent-browser.exe" else "agent-browser" end)
			}}
		},
		services:[
			{name:"agent-runtime",version:$version,order:10,health_url:"http://127.0.0.1:18081/healthz",artifacts:{($platform):{
				url:$runtime_url,sha256:$runtime_sha,size_bytes:$runtime_size,
				sbom_sha256:$zero_hash,signature:{},code_signing:"DEVELOPMENT",format:(if ($platform|startswith("windows-")) then "zip" else "tar.gz" end),executable:$runtime_executable
			}}},
			{name:"agent-runtime-client",version:$version,order:20,health_url:"http://127.0.0.1:8090/healthz",artifacts:{($platform):{
				url:$client_url,sha256:$client_sha,size_bytes:$client_size,
				sbom_sha256:$zero_hash,signature:{},code_signing:"DEVELOPMENT",format:(if ($platform|startswith("windows-")) then "zip" else "tar.gz" end),executable:$client_executable
			}}}
		]
	}' >"$output"

echo "generated development manifest: $output"
