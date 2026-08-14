#!/usr/bin/env sh
set -eu

ASSET_DIR=${ASSET_DIR:-release-assets}
OUTPUT=${OUTPUT:-release-sbom.spdx.json}
RELEASE_ID=${RELEASE_ID:-athena-${TAG:-v1.0.0}}

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

created=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
files=$(mktemp)
trap 'rm -f "$files"' EXIT

find "$ASSET_DIR" -maxdepth 1 -type f -print | LC_ALL=C sort | while IFS= read -r path; do
  name=$(basename "$path")
  digest=$(sha256 "$path")
  jq -cn --arg name "$name" --arg digest "$digest" '{fileName:$name,SPDXID:("SPDXRef-File-"+($name|gsub("[^A-Za-z0-9.-]";"-"))),checksums:[{algorithm:"SHA256",checksumValue:$digest}]}'
done > "$files"

jq -s \
  --arg name "$RELEASE_ID" \
  --arg created "$created" \
  '{spdxVersion:"SPDX-2.3",dataLicense:"CC0-1.0",SPDXID:"SPDXRef-DOCUMENT",name:$name,documentNamespace:("https://github.com/good-fish-man/athena-launcher/releases/"+$name+"/sbom"),creationInfo:{created:$created,creators:["Organization: good-fish-man","Tool: athena-launcher/generate-release-sbom"]},files:.}' \
  "$files" > "$OUTPUT"

echo "generated $OUTPUT"
