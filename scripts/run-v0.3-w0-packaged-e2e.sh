#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
launcher_bin="${ATHENA_W0_LAUNCHER_BIN:?ATHENA_W0_LAUNCHER_BIN must point to a packaged Launcher binary}"
manifest="${ATHENA_W0_MANIFEST:?ATHENA_W0_MANIFEST must point to a development release manifest}"
run_id="${ATHENA_W0_RUN_ID:-v03-w0-packaged-$(date -u '+%Y%m%dT%H%M%SZ')}"
evidence_dir="${ATHENA_W0_EVIDENCE_DIR:-/private/tmp/athena-$run_id}"
home="${ATHENA_W0_HOME:-$evidence_dir/home}"
database_port="${ATHENA_DATABASE_PORT:-25433}"
fixture_port="${ATHENA_W0_FIXTURE_PORT:-28641}"
api_root="http://127.0.0.1:8090/api/agent-runtime-client/v1"
control_api="$api_root/control"
fixture_url="http://127.0.0.1:$fixture_port"
fixture_bin="$evidence_dir/browser-fixture"
launcher_log="$evidence_dir/launcher.txt"
fixture_log="$evidence_dir/browser-fixture.txt"
evidence="$evidence_dir/packaged-e2e-evidence.json"
auth_config="$evidence_dir/curl-auth.conf"

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
[[ -x "$launcher_bin" ]] || { echo "Launcher is not executable: $launcher_bin" >&2; exit 2; }
[[ -f "$manifest" ]] || { echo "manifest is unavailable: $manifest" >&2; exit 2; }
[[ "$database_port" =~ ^[1-9][0-9]*$ ]] || { echo "ATHENA_DATABASE_PORT is invalid" >&2; exit 2; }
[[ "$fixture_port" =~ ^[1-9][0-9]*$ ]] || { echo "ATHENA_W0_FIXTURE_PORT is invalid" >&2; exit 2; }

mkdir -p "$evidence_dir"
chmod 700 "$evidence_dir"

launcher_pid=""
fixture_pid=""
cleanup() {
	set +e
	if [[ -n "$launcher_pid" ]] && kill -0 "$launcher_pid" >/dev/null 2>&1; then
		kill -TERM "$launcher_pid" >/dev/null 2>&1
		wait "$launcher_pid" >/dev/null 2>&1
	fi
	if [[ -n "$fixture_pid" ]] && kill -0 "$fixture_pid" >/dev/null 2>&1; then
		kill -TERM "$fixture_pid" >/dev/null 2>&1
		wait "$fixture_pid" >/dev/null 2>&1
	fi
}
trap cleanup EXIT INT TERM

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

wait_http() {
	local url="$1"
	local attempts="${2:-90}"
	for _ in $(seq 1 "$attempts"); do
		if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

wait_authenticated_device() {
	local attempts="${1:-90}"
	for _ in $(seq 1 "$attempts"); do
		if curl -fsS --max-time 5 --config "$auth_config" "$control_api/devices" 2>/dev/null |
			jq -e '.devices[] | select(.online == true)' >/dev/null; then
			return 0
		fi
		sleep 1
	done
	return 1
}

started_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
started_epoch="$(date '+%s')"

(
	cd "$root"
	go build -o "$fixture_bin" ./scripts/fixtures/browser-v03
)
"$fixture_bin" -addr "127.0.0.1:$fixture_port" >"$fixture_log" 2>&1 &
fixture_pid=$!
wait_http "$fixture_url/health" 30 || { echo "browser fixture did not become healthy" >&2; exit 1; }
kill -0 "$fixture_pid" >/dev/null 2>&1 || { echo "browser fixture exited unexpectedly" >&2; exit 1; }

ATHENA_DATABASE_PORT="$database_port" "$launcher_bin" run --home "$home" --manifest "$manifest" >"$launcher_log" 2>&1 &
launcher_pid=$!
wait_http "http://127.0.0.1:8090/healthz" 180 || { echo "packaged Client did not become healthy" >&2; exit 1; }
kill -0 "$launcher_pid" >/dev/null 2>&1 || { echo "packaged Launcher exited unexpectedly" >&2; exit 1; }

password_file="$home/secrets/bootstrap-admin.password"
for _ in $(seq 1 30); do
	[[ -s "$password_file" ]] && break
	sleep 1
done
[[ -s "$password_file" ]] || { echo "bootstrap admin password was not created" >&2; exit 1; }
chmod 600 "$password_file"

jq -n --arg username athena --rawfile password "$password_file" \
	'{username:$username,password:($password|sub("[\\r\\n]+$";""))}' >"$evidence_dir/login.json"
chmod 600 "$evidence_dir/login.json"
curl -fsS --max-time 15 -H 'Content-Type: application/json' --data-binary "@$evidence_dir/login.json" \
	"$api_root/auth/login" >"$evidence_dir/login-response.json"
access_token="$(jq -er '.data.access_token' "$evidence_dir/login-response.json")"
printf 'header = "Authorization: Bearer %s"\n' "$access_token" >"$auth_config"
chmod 600 "$auth_config" "$evidence_dir/login-response.json"
unset access_token

wait_authenticated_device 90 || { echo "packaged Device Runtime did not register" >&2; exit 1; }
device_id="$(curl -fsS --config "$auth_config" "$control_api/devices" | jq -er '[.devices[] | select(.online == true)][0].id')"
curl -fsS --config "$auth_config" -X POST "$control_api/devices/$device_id/bind" >"$evidence_dir/device-bind.json"
device_before_restart="$(curl -fsS --config "$auth_config" "$control_api/devices" | jq -c --arg id "$device_id" '.devices[] | select(.id==$id)')"
fencing_before_restart="$(jq -er '.fencing_token' <<<"$device_before_restart")"

task_id="$run_id-browser"
action_index=0
last_action_file=""
last_observation_file=""

prepare_action() {
	local label="$1" capability="$2" operation="$3" risk="$4" session_id="$5" arguments="$6"
	local revision=1 sequence=1
	if curl -fsS --max-time 10 --config "$auth_config" "$control_api/tasks/$task_id" >"$evidence_dir/task-current.json" 2>/dev/null; then
		revision="$(jq -er '.revision' "$evidence_dir/task-current.json")"
		sequence="$(jq -er '.sequence + 1' "$evidence_dir/task-current.json")"
	fi
	action_index=$((action_index + 1))
	local action_id="$task_id-$action_index-$label"
	local issued deadline
	issued="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	if date -u -v+5M '+%Y-%m-%dT%H:%M:%SZ' >/dev/null 2>&1; then
		deadline="$(date -u -v+5M '+%Y-%m-%dT%H:%M:%SZ')"
	else
		deadline="$(date -u -d '+5 minutes' '+%Y-%m-%dT%H:%M:%SZ')"
	fi
	last_action_file="$evidence_dir/action-$action_index-$label.json"
	jq -n \
		--arg device "$device_id" --arg task "$task_id" --arg step "step-$action_index-$label" \
		--arg action "$action_id" --arg trace "arc-$task_id" --arg session "$session_id" \
		--arg issued "$issued" --arg deadline "$deadline" --arg capability "$capability" \
		--arg operation "$operation" --arg risk "$risk" --argjson sequence "$sequence" \
		--argjson revision "$revision" --argjson arguments "$arguments" \
		'{device_id:$device,action:({protocol:"athena.agent.v4",type:"ACTION",task_id:$task,
		step_id:$step,action_id:$action,trace_id:$trace,session_id:$session,
		sequence:$sequence,revision:$revision,idempotency_key:($action+"-v1"),issued_at:$issued,
		deadline:$deadline,capability:$capability,operation:$operation,arguments:$arguments,
		policy:{risk:$risk,decision:"ALLOW"}} | if $session=="" then del(.session_id) else . end)}' >"$last_action_file"
	chmod 600 "$last_action_file"
}

dispatch_prepared() {
	local label="$1"
	last_observation_file="$evidence_dir/observation-$action_index-$label.json"
	curl -fsS --max-time 330 --config "$auth_config" -H 'Content-Type: application/json' \
		--data-binary "@$last_action_file" "$control_api/actions" >"$last_observation_file"
	chmod 600 "$last_observation_file"
}

dispatch_action() {
	prepare_action "$@"
	dispatch_prepared "$1"
}

dispatch_action "open-catalog" "browser.open" "navigate" "R0" "" \
	"$(jq -nc --arg url "$fixture_url/catalog" '{url:$url,snapshot:true}')"
open_observation="$last_observation_file"
session_id="$(jq -er 'select(.status=="SUCCEEDED") | .session_id' "$open_observation")"

dispatch_action "play-second" "browser.task" "execute" "R1" "$session_id" \
	'{"goal":"Play the second video on the current page"}'
play_observation="$last_observation_file"
jq -e --arg session "$session_id" --arg url "$fixture_url/video/second" '
	.status=="SUCCEEDED" and .session_id==$session and .state.url==$url and
	.state.playback.playing==true and .state.playback.progressed==true and
	.state.playback.verified==true and .state.browser_task.resolution.requested_ordinal==2' \
	"$play_observation" >/dev/null

dispatch_action "open-reference" "browser.open" "navigate" "R0" "$session_id" \
	"$(jq -nc --arg url "http://localhost:$fixture_port/reference" '{url:$url,snapshot:true}')"
reference_observation="$last_observation_file"
jq -e --arg session "$session_id" '
	.status=="SUCCEEDED" and .session_id==$session and .state.tabs.count>=2' \
	"$reference_observation" >/dev/null
video_tab_id="$(jq -er --arg suffix '/video/second' '.state.tabs.items[] | select(.url|endswith($suffix)) | .id' "$reference_observation")"
reference_tab_id="$(jq -er --arg suffix '/reference' '.state.tabs.items[] | select(.url|endswith($suffix)) | .id' "$reference_observation")"

dispatch_action "close-video-tab" "browser.close" "close" "R1" "$session_id" \
	"$(jq -nc --arg tab "$video_tab_id" '{tab_id:$tab,snapshot:true}')"
close_observation="$last_observation_file"
jq -e --arg session "$session_id" --arg closed "$video_tab_id" --arg remaining "$reference_tab_id" '
	.status=="SUCCEEDED" and .session_id==$session and .state.closed_tab_id==$closed and
	.state.tabs.count==1 and .state.tab_id==$remaining' "$close_observation" >/dev/null

dispatch_action "continue-reference" "browser.task" "execute" "R1" "$session_id" \
	'{"goal":"On the current page, click Continue reference"}'
continue_observation="$last_observation_file"
jq -e --arg suffix '/reference/continued' '
	.status=="SUCCEEDED" and (.state.url|endswith($suffix)) and .state.browser_task.completed==true' \
	"$continue_observation" >/dev/null

dispatch_action "open-submission" "browser.open" "navigate" "R0" "$session_id" \
	"$(jq -nc --arg url "$fixture_url/submission" '{url:$url,snapshot:true}')"
submission_observation="$last_observation_file"
jq -e --arg url "$fixture_url/submission" '.status=="SUCCEEDED" and .state.url==$url' "$submission_observation" >/dev/null
curl -fsS -X POST "$fixture_url/api/submissions/reset" >/dev/null

dispatch_action "submit-idempotent" "browser.task" "execute" "R1" "$session_id" \
	'{"goal":"On the current page, click Submit exactly once"}'
submit_action="$last_action_file"
submit_observation="$last_observation_file"
first_count="$(curl -fsS "$fixture_url/api/submissions" | jq -er '.count')"
replay_observation="$evidence_dir/observation-$action_index-submit-idempotent-replay.json"
curl -fsS --max-time 330 --config "$auth_config" -H 'Content-Type: application/json' \
	--data-binary "@$submit_action" "$control_api/actions" >"$replay_observation"
chmod 600 "$replay_observation"
replay_count="$(curl -fsS "$fixture_url/api/submissions" | jq -er '.count')"
jq -e --arg first "$(jq -r '.observation_id' "$submit_observation")" --argjson count "$replay_count" \
	'.status=="SUCCEEDED" and .observation_id==$first and $count==1' "$replay_observation" >/dev/null
[[ "$first_count" -eq 1 && "$replay_count" -eq 1 ]]

dispatch_action "invalid-url" "browser.navigate" "navigate" "R0" "$session_id" \
	'{"url":"javascript:alert(1)","snapshot":true}'
error_observation="$last_observation_file"
jq -e '.status=="FAILED" and (.error|contains("browser URL must be an absolute HTTP(S) URL without credentials"))' "$error_observation" >/dev/null
grep -F "[arc-$task_id]" "$launcher_log" >/dev/null
grep -F 'span_name=device.execute' "$launcher_log" >/dev/null
grep -F 'span_name=perception.observe' "$launcher_log" >/dev/null
grep -F 'cost_ms=' "$launcher_log" >/dev/null
grep -F 'error_chain=browser URL must be an absolute HTTP(S) URL without credentials' "$launcher_log" >/dev/null

# A fresh request after the original command connection has closed proves that
# the timeline remains recoverable without a frontend process.
curl -fsS --config "$auth_config" "$control_api/tasks/$task_id" >"$evidence_dir/task-before-restart.json"
curl -fsS --config "$auth_config" "$control_api/tasks/$task_id/events?after=0&limit=500" >"$evidence_dir/events-before-restart.json"
curl -fsS --config "$auth_config" "$control_api/tasks/$task_id/world" >"$evidence_dir/world-before-restart.json"
jq -e '(.actions|length)>=8 and (.observations|length)>=8' "$evidence_dir/task-before-restart.json" >/dev/null
jq -e '.events|length>=16' "$evidence_dir/events-before-restart.json" >/dev/null

# Kill only this isolated Launcher's Client child while a device action is in
# flight. Launcher remains alive, finishes the action, journals it, reconnects
# to the replacement Client, and replays one observation.
prepare_action "wait-through-client-restart" "browser.wait" "wait" "R0" "$session_id" '{"value":"6000"}'
restart_action="$last_action_file"
restart_action_id="$(jq -er '.action.action_id' "$restart_action")"
restart_observation_http="$evidence_dir/restart-dispatch-response.json"
set +e
curl -fsS --max-time 330 --config "$auth_config" -H 'Content-Type: application/json' \
	--data-binary "@$restart_action" "$control_api/actions" >"$restart_observation_http" 2>"$evidence_dir/restart-dispatch-error.txt" &
dispatch_pid=$!
sleep 2
old_client_pid="$(ps -axo pid,ppid,command | awk -v parent="$launcher_pid" '$2==parent && /agent-runtime-client/ && first=="" {first=$1} END {print first}')"
[[ -n "$old_client_pid" ]] || { echo "managed Client PID was not found" >&2; exit 1; }
kill -TERM "$old_client_pid"
wait "$dispatch_pid"
restart_http_exit=$?
set -e
wait_http "http://127.0.0.1:8090/healthz" 90 || { echo "replacement Client did not become healthy" >&2; exit 1; }

reconnected=false
new_client_pid=""
device_after_restart=""
for _ in $(seq 1 90); do
	new_client_pid="$(ps -axo pid,ppid,command | awk -v parent="$launcher_pid" -v old="$old_client_pid" '$2==parent && $1!=old && /agent-runtime-client/ && first=="" {first=$1} END {print first}')"
	if [[ -n "$new_client_pid" ]] &&
		device_after_restart="$(curl -fsS --config "$auth_config" "$control_api/devices" 2>/dev/null | jq -c --arg id "$device_id" --argjson fence "$fencing_before_restart" '[.devices[] | select(.id==$id and .online==true and .fencing_token>$fence)][0] // empty')" &&
		[[ -n "$device_after_restart" ]]; then
		reconnected=true
		break
	fi
	sleep 1
done
[[ "$reconnected" == true ]] || { echo "Device Runtime did not reconnect with a newer fencing token" >&2; exit 1; }

recovered=false
for _ in $(seq 1 90); do
	if curl -fsS --config "$auth_config" "$control_api/tasks/$task_id" >"$evidence_dir/task-after-restart.json" 2>/dev/null &&
		jq -e --arg id "$restart_action_id" '.status=="PAUSED" and ([.observations[] | select(.action_id==$id and .status=="SUCCEEDED")]|length==1)' \
		"$evidence_dir/task-after-restart.json" >/dev/null; then
		recovered=true
		break
	fi
	sleep 1
done
[[ "$recovered" == true ]]
curl -fsS --config "$auth_config" "$control_api/tasks/$task_id/events?after=0&limit=500" >"$evidence_dir/events-after-restart.json"
jq -e --arg id "$restart_action_id" '[.events[] | select(.type=="observation.received" and .action_id==$id)]|length==1' "$evidence_dir/events-after-restart.json" >/dev/null
[[ "$(jq -r '.fencing_token' <<<"$device_after_restart")" -gt "$fencing_before_restart" ]]

journal="$home/data/device-action-journal-v4.json"
jq -e --arg id "$restart_action_id" '
	[.completed[] | select(.action_id==$id and .status=="SUCCEEDED" and .finished_at!="0001-01-01T00:00:00Z")]|length==1' \
	"$journal" >/dev/null

finished_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
finished_epoch="$(date '+%s')"
launcher_sha="$(sha256_file "$launcher_bin")"
manifest_sha="$(sha256_file "$manifest")"
transcript_sha="$(sha256_file "$launcher_log")"

jq -n \
	--arg schema "athena.internal.v0.3.w0.packaged-e2e-evidence.v1" --arg run_id "$run_id" \
	--arg started_at "$started_at" --arg finished_at "$finished_at" \
	--arg platform "$(go env GOOS)-$(go env GOARCH)" --arg launcher "$launcher_bin" \
	--arg launcher_sha "$launcher_sha" --arg manifest "$manifest" --arg manifest_sha "$manifest_sha" \
	--arg transcript "$launcher_log" --arg transcript_sha "$transcript_sha" --arg task_id "$task_id" \
	--arg device_id "$device_id" --arg session_id "$session_id" --argjson restart_http_exit "$restart_http_exit" \
	--argjson duration_seconds "$((finished_epoch - started_epoch))" \
	'{schema:$schema,run_id:$run_id,scope:"LOCAL_PACKAGED_CROSS_PROCESS_DARWIN_ARM64",result:"PASS",
	started_at:$started_at,finished_at:$finished_at,duration_seconds:$duration_seconds,platform:$platform,
	artifacts:{launcher:{path:$launcher,sha256:$launcher_sha},manifest:{path:$manifest,sha256:$manifest_sha},transcript:{path:$transcript,sha256:$transcript_sha}},
	correlation:{task_id:$task_id,device_id:$device_id,session_id:$session_id},
	acceptance_scenarios:[
	{id:"v0.2.acceptance.1",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"Second observed video was grounded and playback was verified."},
	{id:"v0.2.acceptance.2",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"Two sites shared one Browser Session and distinct stable tabs."},
	{id:"v0.2.acceptance.3",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"A stable tab id was closed and the surviving tab continued after index shift."},
	{id:"v0.2.acceptance.4",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"No frontend process was started; fresh requests restored the durable task timeline."},
	{id:"v0.2.acceptance.5",status:"PASS",evidence_level:"FAULT_INJECTION",assertion:"Client restart advanced the lease fence and replayed exactly one journaled observation.",original_http_exit:$restart_http_exit},
	{id:"v0.2.acceptance.6",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"Replaying an identical Action returned the same observation and submission count remained one."},
	{id:"v0.2.acceptance.7",status:"PASS",evidence_level:"PACKAGED_CROSS_PROCESS",assertion:"Failure logs contain trace correlation, operation spans, root cause, and duration."}
	],
	limitations:[
	"The service and browser processes are real packaged artifacts; websites are deterministic local fixtures, not production services.",
	"This local development-manifest run is unsigned and does not replace signed macOS, Windows, and Linux installer smoke tests.",
	"The control-plane restart intentionally leaves the recovered task PAUSED for explicit decision-loop resume."
	]}' >"$evidence"
chmod 600 "$evidence"
echo "wrote $evidence"
