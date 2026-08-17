#!/usr/bin/env sh
set -eu

TAG=${TAG:-${GITHUB_REF_NAME:-}}
if [ -z "$TAG" ]; then
  echo "TAG is required (for example TAG=v1.0.0)" >&2
  exit 1
fi
LAUNCHER_TAG=${LAUNCHER_TAG:-$TAG}
POSTGRES_VERSION=${POSTGRES_VERSION:-16.13.0}
AGENT_BROWSER_VERSION=${AGENT_BROWSER_VERSION:-0.34.0}
ASSET_DIR=${ASSET_DIR:-release-assets}
OUTPUT=${OUTPUT:-release-manifest.json}
SBOM_FILE=${SBOM_FILE:-release-sbom.spdx.json}
COMPATIBILITY_FILE=${COMPATIBILITY_FILE:-compatibility-v1.0.json}
MINIMUM_FROM_VERSION=${MINIMUM_FROM_VERSION:-0.9.0}
DARWIN_CODE_SIGNING_STATUS=${DARWIN_CODE_SIGNING_STATUS:-UNAVAILABLE}
WINDOWS_CODE_SIGNING_STATUS=${WINDOWS_CODE_SIGNING_STATUS:-UNAVAILABLE}
LINUX_CODE_SIGNING_STATUS=${LINUX_CODE_SIGNING_STATUS:-CHECKSUM_VERIFIED}
RUNTIME_REPO=${RUNTIME_REPO:-good-fish-man/agent-runtime}
CLIENT_REPO=${CLIENT_REPO:-good-fish-man/agent-runtime-client}
FRONTEND_REPO=${FRONTEND_REPO:-good-fish-man/athena-agent-ui}
LAUNCHER_REPO=${LAUNCHER_REPO:-good-fish-man/athena-launcher}
AGENT_BROWSER_REPO=${AGENT_BROWSER_REPO:-vercel-labs/agent-browser}

if date -u -d '+90 days' '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
  default_expires_at=$(date -u -d '+90 days' '+%Y-%m-%dT%H:%M:%SZ')
else
  default_expires_at=$(date -u -v+90d '+%Y-%m-%dT%H:%M:%SZ')
fi
ISSUED_AT=${ISSUED_AT:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}
EXPIRES_AT=${EXPIRES_AT:-$default_expires_at}

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

if [ ! -f "$SBOM_FILE" ]; then
  echo "release SBOM is required: $SBOM_FILE" >&2
  exit 1
fi

if [ ! -f "$COMPATIBILITY_FILE" ]; then
  echo "release compatibility matrix is required: $COMPATIBILITY_FILE" >&2
  exit 1
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

file_size() {
  wc -c < "$1" | tr -d '[:space:]'
}

require_production_signing_status() {
  case "$1" in
    NOTARIZED|DEVELOPER_ID|AUTHENTICODE|GPG|COSIGN|PACKAGE_SIGNED|CHECKSUM_VERIFIED|THIRD_PARTY_UPSTREAM) ;;
    *)
      echo "unsupported or non-production code signing status: $1" >&2
      exit 1
      ;;
  esac
}

require_production_signing_status "$DARWIN_CODE_SIGNING_STATUS"
require_production_signing_status "$WINDOWS_CODE_SIGNING_STATUS"
require_production_signing_status "$LINUX_CODE_SIGNING_STATUS"

require_asset() {
  if [ ! -f "$ASSET_DIR/$1" ]; then
    echo "required release asset is missing: $ASSET_DIR/$1" >&2
    exit 1
  fi
}

runtime_darwin_arm64="agent-runtime_${TAG}_darwin_arm64.tar.gz"
runtime_darwin_amd64="agent-runtime_${TAG}_darwin_amd64.tar.gz"
runtime_linux_arm64="agent-runtime_${TAG}_linux_arm64.tar.gz"
runtime_linux_amd64="agent-runtime_${TAG}_linux_amd64.tar.gz"
runtime_windows_amd64="agent-runtime_${TAG}_windows_amd64.zip"

client_darwin_arm64="agent-runtime-client_${TAG}_darwin_arm64.tar.gz"
client_darwin_amd64="agent-runtime-client_${TAG}_darwin_amd64.tar.gz"
client_linux_arm64="agent-runtime-client_${TAG}_linux_arm64.tar.gz"
client_linux_amd64="agent-runtime-client_${TAG}_linux_amd64.tar.gz"
client_windows_amd64="agent-runtime-client_${TAG}_windows_amd64.zip"

postgres_darwin_arm64="postgres_${POSTGRES_VERSION}_darwin_arm64.tar.gz"
postgres_darwin_amd64="postgres_${POSTGRES_VERSION}_darwin_amd64.tar.gz"
postgres_linux_arm64="postgres_${POSTGRES_VERSION}_linux_arm64.tar.gz"
postgres_linux_amd64="postgres_${POSTGRES_VERSION}_linux_amd64.tar.gz"
postgres_windows_amd64="postgres_${POSTGRES_VERSION}_windows_amd64.tar.gz"
frontend_asset="athena-agent-ui_${TAG}.tar.gz"
browser_darwin_arm64="agent-browser-darwin-arm64"
browser_darwin_amd64="agent-browser-darwin-x64"
browser_linux_arm64="agent-browser-linux-arm64"
browser_linux_amd64="agent-browser-linux-x64"
browser_windows_amd64="agent-browser-win32-x64.exe"

for asset in \
  "$runtime_darwin_arm64" "$runtime_darwin_amd64" "$runtime_linux_arm64" "$runtime_linux_amd64" "$runtime_windows_amd64" \
  "$client_darwin_arm64" "$client_darwin_amd64" "$client_linux_arm64" "$client_linux_amd64" "$client_windows_amd64" \
  "$postgres_darwin_arm64" "$postgres_darwin_amd64" "$postgres_linux_arm64" "$postgres_linux_amd64" "$postgres_windows_amd64" \
  "$frontend_asset" \
  "$browser_darwin_arm64" "$browser_darwin_amd64" "$browser_linux_arm64" "$browser_linux_amd64" "$browser_windows_amd64"
do
  require_asset "$asset"
done

release_url() {
  repo=$1
  tag=$2
  asset=$3
  printf 'https://github.com/%s/releases/download/%s/%s' "$repo" "$tag" "$asset"
}

jq -n \
	--arg schema "athena.release-manifest.v1" \
	--arg release_id "athena-$TAG" \
	--arg version "${TAG#v}" \
	--arg protocol_version "1.0.0" \
	--arg minimum_from_version "$MINIMUM_FROM_VERSION" \
	--arg sbom_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "release-sbom.spdx.json")" \
	--arg sbom_sha "$(sha256 "$SBOM_FILE")" \
	--arg compatibility_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "compatibility-v1.0.json")" \
	--arg compatibility_sha "$(sha256 "$COMPATIBILITY_FILE")" \
	--arg issued_at "$ISSUED_AT" \
	--arg expires_at "$EXPIRES_AT" \
	--arg darwin_signing "$DARWIN_CODE_SIGNING_STATUS" \
	--arg windows_signing "$WINDOWS_CODE_SIGNING_STATUS" \
	--arg linux_signing "$LINUX_CODE_SIGNING_STATUS" \
  --arg tag "$TAG" \
  --arg postgres_version "$POSTGRES_VERSION" \
  --arg browser_version "$AGENT_BROWSER_VERSION" \
	  --arg p_da_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_darwin_arm64")" --arg p_da_sha "$(sha256 "$ASSET_DIR/$postgres_darwin_arm64")" --argjson p_da_size "$(file_size "$ASSET_DIR/$postgres_darwin_arm64")" \
	  --arg p_dx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_darwin_amd64")" --arg p_dx_sha "$(sha256 "$ASSET_DIR/$postgres_darwin_amd64")" --argjson p_dx_size "$(file_size "$ASSET_DIR/$postgres_darwin_amd64")" \
	  --arg p_la_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_linux_arm64")" --arg p_la_sha "$(sha256 "$ASSET_DIR/$postgres_linux_arm64")" --argjson p_la_size "$(file_size "$ASSET_DIR/$postgres_linux_arm64")" \
	  --arg p_lx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_linux_amd64")" --arg p_lx_sha "$(sha256 "$ASSET_DIR/$postgres_linux_amd64")" --argjson p_lx_size "$(file_size "$ASSET_DIR/$postgres_linux_amd64")" \
	  --arg p_wx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_windows_amd64")" --arg p_wx_sha "$(sha256 "$ASSET_DIR/$postgres_windows_amd64")" --argjson p_wx_size "$(file_size "$ASSET_DIR/$postgres_windows_amd64")" \
	  --arg r_da_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_darwin_arm64")" --arg r_da_sha "$(sha256 "$ASSET_DIR/$runtime_darwin_arm64")" --argjson r_da_size "$(file_size "$ASSET_DIR/$runtime_darwin_arm64")" \
	  --arg r_dx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_darwin_amd64")" --arg r_dx_sha "$(sha256 "$ASSET_DIR/$runtime_darwin_amd64")" --argjson r_dx_size "$(file_size "$ASSET_DIR/$runtime_darwin_amd64")" \
	  --arg r_la_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_linux_arm64")" --arg r_la_sha "$(sha256 "$ASSET_DIR/$runtime_linux_arm64")" --argjson r_la_size "$(file_size "$ASSET_DIR/$runtime_linux_arm64")" \
	  --arg r_lx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_linux_amd64")" --arg r_lx_sha "$(sha256 "$ASSET_DIR/$runtime_linux_amd64")" --argjson r_lx_size "$(file_size "$ASSET_DIR/$runtime_linux_amd64")" \
	  --arg r_wx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_windows_amd64")" --arg r_wx_sha "$(sha256 "$ASSET_DIR/$runtime_windows_amd64")" --argjson r_wx_size "$(file_size "$ASSET_DIR/$runtime_windows_amd64")" \
	  --arg c_da_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_darwin_arm64")" --arg c_da_sha "$(sha256 "$ASSET_DIR/$client_darwin_arm64")" --argjson c_da_size "$(file_size "$ASSET_DIR/$client_darwin_arm64")" \
	  --arg c_dx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_darwin_amd64")" --arg c_dx_sha "$(sha256 "$ASSET_DIR/$client_darwin_amd64")" --argjson c_dx_size "$(file_size "$ASSET_DIR/$client_darwin_amd64")" \
	  --arg c_la_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_linux_arm64")" --arg c_la_sha "$(sha256 "$ASSET_DIR/$client_linux_arm64")" --argjson c_la_size "$(file_size "$ASSET_DIR/$client_linux_arm64")" \
	  --arg c_lx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_linux_amd64")" --arg c_lx_sha "$(sha256 "$ASSET_DIR/$client_linux_amd64")" --argjson c_lx_size "$(file_size "$ASSET_DIR/$client_linux_amd64")" \
	  --arg c_wx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_windows_amd64")" --arg c_wx_sha "$(sha256 "$ASSET_DIR/$client_windows_amd64")" --argjson c_wx_size "$(file_size "$ASSET_DIR/$client_windows_amd64")" \
	  --arg frontend_url "$(release_url "$FRONTEND_REPO" "$TAG" "$frontend_asset")" --arg frontend_sha "$(sha256 "$ASSET_DIR/$frontend_asset")" --argjson frontend_size "$(file_size "$ASSET_DIR/$frontend_asset")" \
	  --arg b_da_url "$(release_url "$AGENT_BROWSER_REPO" "v$AGENT_BROWSER_VERSION" "$browser_darwin_arm64")" --arg b_da_sha "$(sha256 "$ASSET_DIR/$browser_darwin_arm64")" --argjson b_da_size "$(file_size "$ASSET_DIR/$browser_darwin_arm64")" \
	  --arg b_dx_url "$(release_url "$AGENT_BROWSER_REPO" "v$AGENT_BROWSER_VERSION" "$browser_darwin_amd64")" --arg b_dx_sha "$(sha256 "$ASSET_DIR/$browser_darwin_amd64")" --argjson b_dx_size "$(file_size "$ASSET_DIR/$browser_darwin_amd64")" \
	  --arg b_la_url "$(release_url "$AGENT_BROWSER_REPO" "v$AGENT_BROWSER_VERSION" "$browser_linux_arm64")" --arg b_la_sha "$(sha256 "$ASSET_DIR/$browser_linux_arm64")" --argjson b_la_size "$(file_size "$ASSET_DIR/$browser_linux_arm64")" \
	  --arg b_lx_url "$(release_url "$AGENT_BROWSER_REPO" "v$AGENT_BROWSER_VERSION" "$browser_linux_amd64")" --arg b_lx_sha "$(sha256 "$ASSET_DIR/$browser_linux_amd64")" --argjson b_lx_size "$(file_size "$ASSET_DIR/$browser_linux_amd64")" \
	  --arg b_wx_url "$(release_url "$AGENT_BROWSER_REPO" "v$AGENT_BROWSER_VERSION" "$browser_windows_amd64")" --arg b_wx_sha "$(sha256 "$ASSET_DIR/$browser_windows_amd64")" --argjson b_wx_size "$(file_size "$ASSET_DIR/$browser_windows_amd64")" \
	'def enrich:
	  with_entries(.key as $platform | .value += {
	    sbom_sha256: $sbom_sha,
	    code_signing: (if ($platform|startswith("darwin-")) then $darwin_signing elif ($platform|startswith("windows-")) then $windows_signing else $linux_signing end),
	    signature: {algorithm:"Ed25519",key_id:"",value:""}
	  });
	{
	  schema: $schema,
	  release_id: $release_id,
	  version: $version,
	  protocol_version: $protocol_version,
	  minimum_from_version: $minimum_from_version,
	  sbom_url: $sbom_url,
	  sbom_sha256: $sbom_sha,
	  compatibility_url: $compatibility_url,
	  compatibility_sha256: $compatibility_sha,
	  signature: {algorithm:"Ed25519",key_id:"",value:""},
	  issued_at: $issued_at,
	  expires_at: $expires_at,
    database: {
      version: $postgres_version,
      bin_dir: "bin",
      artifacts: {
	        "darwin-arm64": {url: $p_da_url, sha256: $p_da_sha, size_bytes: $p_da_size, format: "tar.gz", executable: ""},
	        "darwin-amd64": {url: $p_dx_url, sha256: $p_dx_sha, size_bytes: $p_dx_size, format: "tar.gz", executable: ""},
	        "linux-arm64": {url: $p_la_url, sha256: $p_la_sha, size_bytes: $p_la_size, format: "tar.gz", executable: ""},
	        "linux-amd64": {url: $p_lx_url, sha256: $p_lx_sha, size_bytes: $p_lx_size, format: "tar.gz", executable: ""},
	        "windows-amd64": {url: $p_wx_url, sha256: $p_wx_sha, size_bytes: $p_wx_size, format: "tar.gz", executable: ""}
      }
    },
    browser: {
      version: $browser_version,
      artifacts: {
	        "darwin-arm64": {url: $b_da_url, sha256: $b_da_sha, size_bytes: $b_da_size, format: "raw", executable: "agent-browser"},
	        "darwin-amd64": {url: $b_dx_url, sha256: $b_dx_sha, size_bytes: $b_dx_size, format: "raw", executable: "agent-browser"},
	        "linux-arm64": {url: $b_la_url, sha256: $b_la_sha, size_bytes: $b_la_size, format: "raw", executable: "agent-browser"},
	        "linux-amd64": {url: $b_lx_url, sha256: $b_lx_sha, size_bytes: $b_lx_size, format: "raw", executable: "agent-browser"},
	        "windows-amd64": {url: $b_wx_url, sha256: $b_wx_sha, size_bytes: $b_wx_size, format: "raw", executable: "agent-browser.exe"}
      }
    },
    frontend: {
      version: $version,
      listen_addr: "127.0.0.1:3000",
      root: "",
      artifacts: {
	        "darwin-arm64": {url: $frontend_url, sha256: $frontend_sha, size_bytes: $frontend_size, format: "tar.gz", executable: ""},
	        "darwin-amd64": {url: $frontend_url, sha256: $frontend_sha, size_bytes: $frontend_size, format: "tar.gz", executable: ""},
	        "linux-arm64": {url: $frontend_url, sha256: $frontend_sha, size_bytes: $frontend_size, format: "tar.gz", executable: ""},
	        "linux-amd64": {url: $frontend_url, sha256: $frontend_sha, size_bytes: $frontend_size, format: "tar.gz", executable: ""},
	        "windows-amd64": {url: $frontend_url, sha256: $frontend_sha, size_bytes: $frontend_size, format: "tar.gz", executable: ""}
      }
    },
    services: [
      {
        name: "agent-runtime",
        version: $version,
        order: 10,
        health_url: "http://127.0.0.1:18081/healthz",
        artifacts: {
	          "darwin-arm64": {url: $r_da_url, sha256: $r_da_sha, size_bytes: $r_da_size, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_darwin_arm64/agent-runtime")},
	          "darwin-amd64": {url: $r_dx_url, sha256: $r_dx_sha, size_bytes: $r_dx_size, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_darwin_amd64/agent-runtime")},
	          "linux-arm64": {url: $r_la_url, sha256: $r_la_sha, size_bytes: $r_la_size, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_linux_arm64/agent-runtime")},
	          "linux-amd64": {url: $r_lx_url, sha256: $r_lx_sha, size_bytes: $r_lx_size, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_linux_amd64/agent-runtime")},
	          "windows-amd64": {url: $r_wx_url, sha256: $r_wx_sha, size_bytes: $r_wx_size, format: "zip", executable: ("agent-runtime_" + $tag + "_windows_amd64/agent-runtime.exe")}
        }
      },
      {
        name: "agent-runtime-client",
        version: $version,
        order: 20,
        health_url: "http://127.0.0.1:8090/healthz",
        artifacts: {
	          "darwin-arm64": {url: $c_da_url, sha256: $c_da_sha, size_bytes: $c_da_size, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_darwin_arm64/agent-runtime-client")},
	          "darwin-amd64": {url: $c_dx_url, sha256: $c_dx_sha, size_bytes: $c_dx_size, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_darwin_amd64/agent-runtime-client")},
	          "linux-arm64": {url: $c_la_url, sha256: $c_la_sha, size_bytes: $c_la_size, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_linux_arm64/agent-runtime-client")},
	          "linux-amd64": {url: $c_lx_url, sha256: $c_lx_sha, size_bytes: $c_lx_size, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_linux_amd64/agent-runtime-client")},
	          "windows-amd64": {url: $c_wx_url, sha256: $c_wx_sha, size_bytes: $c_wx_size, format: "zip", executable: ("agent-runtime-client_" + $tag + "_windows_amd64/agent-runtime-client.exe")}
        }
      }
    ]
	  }
	| .database.artifacts |= enrich
	| if .browser then .browser.artifacts |= enrich else . end
	| .services |= map(.artifacts |= enrich)
	| if .frontend then .frontend.artifacts |= enrich else . end' > "$OUTPUT"

echo "generated $OUTPUT"
