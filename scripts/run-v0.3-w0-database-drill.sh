#!/usr/bin/env sh
set -eu

POSTGRES_ROOT=${ATHENA_W0_POSTGRES_ROOT:?ATHENA_W0_POSTGRES_ROOT must point to an extracted PostgreSQL package}
POSTGRES_ARCHIVE=${ATHENA_W0_POSTGRES_ARCHIVE:?ATHENA_W0_POSTGRES_ARCHIVE must point to the packaged PostgreSQL archive}
EVIDENCE_DIR=${ATHENA_W0_EVIDENCE_DIR:-dist/v0.3-w0-database-drill}
RUN_ID=${ATHENA_W0_RUN_ID:-v03-w0-database-$(date -u +%Y%m%dT%H%M%SZ)}
TRANSCRIPT="$EVIDENCE_DIR/database-drill.txt"
EVIDENCE="$EVIDENCE_DIR/database-evidence.json"

if [ ! -d "$POSTGRES_ROOT" ] || [ ! -x "$POSTGRES_ROOT/bin/postgres" ]; then
	echo "ATHENA_W0_POSTGRES_ROOT is not an extracted runnable PostgreSQL package: $POSTGRES_ROOT" >&2
	exit 2
fi
if [ ! -f "$POSTGRES_ARCHIVE" ]; then
	echo "ATHENA_W0_POSTGRES_ARCHIVE is not a regular file: $POSTGRES_ARCHIVE" >&2
	exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
	echo "jq is required to write canonical drill evidence" >&2
	exit 2
fi

mkdir -p "$EVIDENCE_DIR"
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
set +e
ATHENA_W0_POSTGRES_ROOT="$POSTGRES_ROOT" go test ./internal/launcher/deployment \
	-run TestManagedPostgresBackupUpgradeRollbackDrill -count=1 -v >"$TRANSCRIPT" 2>&1
test_status=$?
set -e
cat "$TRANSCRIPT"

if command -v sha256sum >/dev/null 2>&1; then
	archive_sha=$(sha256sum "$POSTGRES_ARCHIVE" | awk '{print $1}')
	transcript_sha=$(sha256sum "$TRANSCRIPT" | awk '{print $1}')
else
	archive_sha=$(shasum -a 256 "$POSTGRES_ARCHIVE" | awk '{print $1}')
	transcript_sha=$(shasum -a 256 "$TRANSCRIPT" | awk '{print $1}')
fi

result="FAIL"
if [ "$test_status" -eq 0 ] && grep -q 'health=start-stop-pass result=PASS' "$TRANSCRIPT"; then
	result="PASS"
fi
finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
goos=$(go env GOOS)
goarch=$(go env GOARCH)

jq -n \
	--arg schema "athena.internal.v0.3.w0.database-evidence.v1" \
	--arg run_id "$RUN_ID" \
	--arg scope "ISOLATED_PRODUCTION_LIKE_COPY" \
	--arg result "$result" \
	--arg started_at "$started_at" \
	--arg finished_at "$finished_at" \
	--arg platform "$goos-$goarch" \
	--arg postgres_archive "$POSTGRES_ARCHIVE" \
	--arg postgres_archive_sha256 "$archive_sha" \
	--arg transcript "$TRANSCRIPT" \
	--arg transcript_sha256 "$transcript_sha" \
	'{
		schema: $schema,
		run_id: $run_id,
		scope: $scope,
		result: $result,
		started_at: $started_at,
		finished_at: $finished_at,
		platform: $platform,
		postgres_archive: {path: $postgres_archive, sha256: $postgres_archive_sha256},
		transcript: {path: $transcript, sha256: $transcript_sha256},
		assertions: [
			{id: "embedded_server_start", status: $result},
			{id: "encrypted_recovery_create_verify", status: $result},
			{id: "schema_and_data_upgrade_probe", status: $result},
			{id: "authenticated_restore", status: $result},
			{id: "pre_upgrade_value_recovered", status: $result},
			{id: "restored_server_health", status: $result}
		],
		limitations: [
			"This drill uses an isolated copy and does not mutate the user database.",
			"It validates schema/data rollback within the packaged PostgreSQL major version; major-version pg_upgrade remains a later production-hardening gate."
		]
	}' >"$EVIDENCE"

echo "wrote $EVIDENCE"
if [ "$result" != "PASS" ]; then
	exit 1
fi
