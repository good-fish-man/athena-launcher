#!/usr/bin/env sh
set -eu

if [ "$#" -ne 4 ]; then
  echo "usage: $0 BASELINE OUTPUT RELEASE_VERSION MINIMUM_UPGRADE_VERSION" >&2
  exit 1
fi

baseline=$1
output=$2
release_version=${3#v}
minimum_upgrade_version=${4#v}

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi
if [ ! -f "$baseline" ]; then
  echo "compatibility matrix baseline is missing: $baseline" >&2
  exit 1
fi
for version in "$release_version" "$minimum_upgrade_version"; do
  if ! jq -en --arg version "$version" '$version | test("^[0-9]+\\.[0-9]+\\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$")' >/dev/null; then
    echo "invalid semantic version: $version" >&2
    exit 1
  fi
done

protocol_version=$(jq -er '.protocol_version | select(type == "string" and length > 0)' "$baseline")
release_core=${release_version%%[-+]*}
release_major=${release_core%%.*}
release_remainder=${release_core#*.}
release_minor=${release_remainder%%.*}
maximum_peer_version="${release_major}.${release_minor}.999"
generated_at=${GENERATED_AT:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}

tmp=$(mktemp "${output}.tmp.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM

jq \
  --arg release_version "$release_version" \
  --arg protocol_version "$protocol_version" \
  --arg minimum_upgrade_version "$minimum_upgrade_version" \
  --arg maximum_peer_version "$maximum_peer_version" \
  --arg generated_at "$generated_at" \
  '
    .release_version = $release_version
    | .minimum_upgrade_version = $minimum_upgrade_version
    | .generated_at = $generated_at
    | .components |= map(
        .version = (if .component == "athena-protocol" then $protocol_version else $release_version end)
        | .maximum_peer_version = $maximum_peer_version
      )
  ' "$baseline" > "$tmp"

jq -e \
  --arg release_version "$release_version" \
  --arg protocol_version "$protocol_version" \
  --arg minimum_upgrade_version "$minimum_upgrade_version" \
  '
    .schema == "athena.ga.v1"
    and .release_version == $release_version
    and .protocol_version == $protocol_version
    and .minimum_upgrade_version == $minimum_upgrade_version
    and ([.components[].component] | sort == ["agent-runtime", "agent-runtime-client", "agent-ui", "athena-launcher", "athena-protocol"])
    and (all(.components[]; .version == (if .component == "athena-protocol" then $protocol_version else $release_version end)))
  ' "$tmp" >/dev/null

mv "$tmp" "$output"
trap - EXIT HUP INT TERM
echo "materialized $output for release $release_version on protocol $protocol_version"
