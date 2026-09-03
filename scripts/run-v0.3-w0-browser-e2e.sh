#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
browser_bin="${ATHENA_AGENT_BROWSER_BIN:-$HOME/.athena/browser/0.33.1/agent-browser}"
runs="${ATHENA_W0_BROWSER_RUNS:-1}"
run_id="${ATHENA_W0_RUN_ID:-v03-w0-browser-$(date -u '+%Y%m%dT%H%M%SZ')}"
evidence_dir="${ATHENA_W0_EVIDENCE_DIR:-/private/tmp/athena-$run_id}"
transcript="$evidence_dir/browser-e2e.txt"
evidence="$evidence_dir/browser-evidence.json"

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
[[ "$runs" =~ ^[1-9][0-9]*$ ]] || { echo "ATHENA_W0_BROWSER_RUNS must be a positive integer" >&2; exit 2; }
[[ -x "$browser_bin" ]] || { echo "agent-browser is not executable: $browser_bin" >&2; exit 2; }
mkdir -p "$evidence_dir"

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

started_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
started_epoch="$(date '+%s')"
set +e
(
	cd "$root"
	env \
		ATHENA_BROWSER_E2E=1 \
		ATHENA_AGENT_BROWSER_BIN="$browser_bin" \
		go test ./internal/runtime-system/browser-runtime \
			-run '^TestE2EBrowserV3KeepsSessionAndSelectsSecondResult$' \
			-count="$runs" -v
) >"$transcript" 2>&1
test_status=$?
set -e
cat "$transcript"

finished_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
finished_epoch="$(date '+%s')"
passed_runs="$(grep -c -- '--- PASS: TestE2EBrowserV3KeepsSessionAndSelectsSecondResult' "$transcript" || true)"
result="FAIL"
if [[ "$test_status" -eq 0 && "$passed_runs" -eq "$runs" ]]; then
	result="PASS"
fi

jq -n \
	--arg schema "athena.internal.v0.3.w0.browser-evidence.v1" \
	--arg run_id "$run_id" \
	--arg result "$result" \
	--arg started_at "$started_at" \
	--arg finished_at "$finished_at" \
	--arg platform "$(go env GOOS)-$(go env GOARCH)" \
	--arg browser_bin "$browser_bin" \
	--arg browser_sha256 "$(sha256_file "$browser_bin")" \
	--arg transcript "$transcript" \
	--arg transcript_sha256 "$(sha256_file "$transcript")" \
	--argjson requested_runs "$runs" \
	--argjson passed_runs "$passed_runs" \
	--argjson duration_seconds "$((finished_epoch - started_epoch))" \
	'{
		schema: $schema,
		run_id: $run_id,
		scope: "REAL_LOCAL_CDP_BROWSER_WITH_DETERMINISTIC_FIXTURE",
		result: $result,
		started_at: $started_at,
		finished_at: $finished_at,
		duration_seconds: $duration_seconds,
		platform: $platform,
		runs: {requested: $requested_runs, passed: $passed_runs},
		browser: {path: $browser_bin, sha256: $browser_sha256},
		transcript: {path: $transcript, sha256: $transcript_sha256},
		acceptance_scenarios: [
			{id: "v0.2.acceptance.1", status: $result, evidence_level: "REAL_BROWSER_E2E", assertion: "The second observed video is grounded, opened, and playback is verified."},
			{id: "v0.2.acceptance.2", status: $result, evidence_level: "REAL_BROWSER_E2E", assertion: "Two sites use one Browser Session and multiple tabs rather than multiple browser instances."},
			{id: "v0.2.acceptance.3", status: $result, evidence_level: "REAL_BROWSER_E2E", assertion: "After an external tab close shifts tab order, the surviving stable tab id is reused without a duplicate tab and a semantic click continues on the correct page."}
		],
		limitations: [
			"The browser process and CDP execution are real; page content is a deterministic local fixture, not a production website.",
			"This evidence does not replace signed packaged-install smoke tests or the seven-scenario cross-process release run."
		]
	}' >"$evidence"

echo "wrote $evidence"
if [[ "$result" != "PASS" ]]; then
	exit 1
fi
