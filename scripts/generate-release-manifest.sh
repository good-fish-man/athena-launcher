#!/usr/bin/env sh
set -eu

TAG=${TAG:-v0.1.1}
LAUNCHER_TAG=${LAUNCHER_TAG:-$TAG}
POSTGRES_VERSION=${POSTGRES_VERSION:-16.13.0}
ASSET_DIR=${ASSET_DIR:-release-assets}
OUTPUT=${OUTPUT:-release-manifest.json}
RUNTIME_REPO=${RUNTIME_REPO:-good-fish-man/agent-runtime}
CLIENT_REPO=${CLIENT_REPO:-good-fish-man/agent-runtime-client}
FRONTEND_REPO=${FRONTEND_REPO:-good-fish-man/athena-agent-ui}
LAUNCHER_REPO=${LAUNCHER_REPO:-good-fish-man/athena-launcher}

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

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

for asset in \
  "$runtime_darwin_arm64" "$runtime_darwin_amd64" "$runtime_linux_arm64" "$runtime_linux_amd64" "$runtime_windows_amd64" \
  "$client_darwin_arm64" "$client_darwin_amd64" "$client_linux_arm64" "$client_linux_amd64" "$client_windows_amd64" \
  "$postgres_darwin_arm64" "$postgres_darwin_amd64" "$postgres_linux_arm64" "$postgres_linux_amd64" "$postgres_windows_amd64" \
  "$frontend_asset"
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
  --arg version "${TAG#v}" \
  --arg tag "$TAG" \
  --arg postgres_version "$POSTGRES_VERSION" \
  --arg p_da_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_darwin_arm64")" --arg p_da_sha "$(sha256 "$ASSET_DIR/$postgres_darwin_arm64")" \
  --arg p_dx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_darwin_amd64")" --arg p_dx_sha "$(sha256 "$ASSET_DIR/$postgres_darwin_amd64")" \
  --arg p_la_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_linux_arm64")" --arg p_la_sha "$(sha256 "$ASSET_DIR/$postgres_linux_arm64")" \
  --arg p_lx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_linux_amd64")" --arg p_lx_sha "$(sha256 "$ASSET_DIR/$postgres_linux_amd64")" \
  --arg p_wx_url "$(release_url "$LAUNCHER_REPO" "$LAUNCHER_TAG" "$postgres_windows_amd64")" --arg p_wx_sha "$(sha256 "$ASSET_DIR/$postgres_windows_amd64")" \
  --arg r_da_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_darwin_arm64")" --arg r_da_sha "$(sha256 "$ASSET_DIR/$runtime_darwin_arm64")" \
  --arg r_dx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_darwin_amd64")" --arg r_dx_sha "$(sha256 "$ASSET_DIR/$runtime_darwin_amd64")" \
  --arg r_la_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_linux_arm64")" --arg r_la_sha "$(sha256 "$ASSET_DIR/$runtime_linux_arm64")" \
  --arg r_lx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_linux_amd64")" --arg r_lx_sha "$(sha256 "$ASSET_DIR/$runtime_linux_amd64")" \
  --arg r_wx_url "$(release_url "$RUNTIME_REPO" "$TAG" "$runtime_windows_amd64")" --arg r_wx_sha "$(sha256 "$ASSET_DIR/$runtime_windows_amd64")" \
  --arg c_da_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_darwin_arm64")" --arg c_da_sha "$(sha256 "$ASSET_DIR/$client_darwin_arm64")" \
  --arg c_dx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_darwin_amd64")" --arg c_dx_sha "$(sha256 "$ASSET_DIR/$client_darwin_amd64")" \
  --arg c_la_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_linux_arm64")" --arg c_la_sha "$(sha256 "$ASSET_DIR/$client_linux_arm64")" \
  --arg c_lx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_linux_amd64")" --arg c_lx_sha "$(sha256 "$ASSET_DIR/$client_linux_amd64")" \
  --arg c_wx_url "$(release_url "$CLIENT_REPO" "$TAG" "$client_windows_amd64")" --arg c_wx_sha "$(sha256 "$ASSET_DIR/$client_windows_amd64")" \
  --arg frontend_url "$(release_url "$FRONTEND_REPO" "$TAG" "$frontend_asset")" --arg frontend_sha "$(sha256 "$ASSET_DIR/$frontend_asset")" \
  '{
    version: $version,
    database: {
      version: $postgres_version,
      bin_dir: "bin",
      artifacts: {
        "darwin-arm64": {url: $p_da_url, sha256: $p_da_sha, format: "tar.gz", executable: ""},
        "darwin-amd64": {url: $p_dx_url, sha256: $p_dx_sha, format: "tar.gz", executable: ""},
        "linux-arm64": {url: $p_la_url, sha256: $p_la_sha, format: "tar.gz", executable: ""},
        "linux-amd64": {url: $p_lx_url, sha256: $p_lx_sha, format: "tar.gz", executable: ""},
        "windows-amd64": {url: $p_wx_url, sha256: $p_wx_sha, format: "tar.gz", executable: ""}
      }
    },
    frontend: {
      listen_addr: "127.0.0.1:3000",
      root: "",
      artifacts: {
        "darwin-arm64": {url: $frontend_url, sha256: $frontend_sha, format: "tar.gz", executable: ""},
        "darwin-amd64": {url: $frontend_url, sha256: $frontend_sha, format: "tar.gz", executable: ""},
        "linux-arm64": {url: $frontend_url, sha256: $frontend_sha, format: "tar.gz", executable: ""},
        "linux-amd64": {url: $frontend_url, sha256: $frontend_sha, format: "tar.gz", executable: ""},
        "windows-amd64": {url: $frontend_url, sha256: $frontend_sha, format: "tar.gz", executable: ""}
      }
    },
    services: [
      {
        name: "agent-runtime",
        order: 10,
        health_url: "http://127.0.0.1:18081/healthz",
        artifacts: {
          "darwin-arm64": {url: $r_da_url, sha256: $r_da_sha, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_darwin_arm64/agent-runtime")},
          "darwin-amd64": {url: $r_dx_url, sha256: $r_dx_sha, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_darwin_amd64/agent-runtime")},
          "linux-arm64": {url: $r_la_url, sha256: $r_la_sha, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_linux_arm64/agent-runtime")},
          "linux-amd64": {url: $r_lx_url, sha256: $r_lx_sha, format: "tar.gz", executable: ("agent-runtime_" + $tag + "_linux_amd64/agent-runtime")},
          "windows-amd64": {url: $r_wx_url, sha256: $r_wx_sha, format: "zip", executable: ("agent-runtime_" + $tag + "_windows_amd64/agent-runtime.exe")}
        }
      },
      {
        name: "agent-runtime-client",
        order: 20,
        health_url: "http://127.0.0.1:8090/healthz",
        artifacts: {
          "darwin-arm64": {url: $c_da_url, sha256: $c_da_sha, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_darwin_arm64/agent-runtime-client")},
          "darwin-amd64": {url: $c_dx_url, sha256: $c_dx_sha, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_darwin_amd64/agent-runtime-client")},
          "linux-arm64": {url: $c_la_url, sha256: $c_la_sha, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_linux_arm64/agent-runtime-client")},
          "linux-amd64": {url: $c_lx_url, sha256: $c_lx_sha, format: "tar.gz", executable: ("agent-runtime-client_" + $tag + "_linux_amd64/agent-runtime-client")},
          "windows-amd64": {url: $c_wx_url, sha256: $c_wx_sha, format: "zip", executable: ("agent-runtime-client_" + $tag + "_windows_amd64/agent-runtime-client.exe")}
        }
      }
    ]
  }' > "$OUTPUT"

echo "generated $OUTPUT"
