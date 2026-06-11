#!/usr/bin/env bash
set -euo pipefail

API_URL="${SYSBOX_E2E_API_URL:-${API_URL:-http://127.0.0.1:9876}}"
TOKEN="${SYSBOX_API_TOKEN:-}"
AGENT_ID="${SYSBOX_E2E_AGENT_ID:-${AGENT_ID:-local-docker}}"
RUN_TIMEOUT_SECONDS="${SYSBOX_E2E_RUN_TIMEOUT_SECONDS:-120}"

suffix="$(date +%s)-$$"
topology="api-e2e-${suffix}"
hcl_name_suffix="$(printf '%s' "$suffix" | tr '-' '_')"
node_name="web_${hcl_name_suffix}"
image_name="nginx_${hcl_name_suffix}"
network_name="app_${hcl_name_suffix}"

auth_args=()
if [[ -n "$TOKEN" ]]; then
	auth_args=(-H "Authorization: Bearer ${TOKEN}")
fi

log() {
	printf '[e2e] %s\n' "$*"
}

fail() {
	printf '[e2e] ERROR: %s\n' "$*" >&2
	exit 1
}

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

curl_json() {
	local method="$1"
	local path="$2"
	local body="${3:-}"
	local tmp
	tmp="$(mktemp)"
	local code
	if [[ -n "$body" ]]; then
		code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
			"${auth_args[@]}" \
			-H 'Content-Type: application/json' \
			-d "$body" \
			"${API_URL}${path}")"
	else
		code="$(curl -sS -o "$tmp" -w '%{http_code}' -X "$method" \
			"${auth_args[@]}" \
			"${API_URL}${path}")"
	fi
	cat "$tmp"
	rm -f "$tmp"
	printf '\n%s\n' "$code"
}

expect_status() {
	local response="$1"
	local want="$2"
	local code
	code="$(printf '%s' "$response" | tail -n1)"
	if [[ "$code" != "$want" ]]; then
		printf '%s\n' "$response" >&2
		fail "expected HTTP $want, got $code"
	fi
	printf '%s' "$response" | sed '$d'
}

api_get() {
	expect_status "$(curl_json GET "$1")" "${2:-200}"
}

api_post() {
	expect_status "$(curl_json POST "$1" "${2:-}")" "${3:-200}"
}

api_delete() {
	expect_status "$(curl_json DELETE "$1")" "${2:-200}"
}

wait_run() {
	local run_id="$1"
	local deadline=$((SECONDS + RUN_TIMEOUT_SECONDS))
	local status body err
	while (( SECONDS < deadline )); do
		body="$(api_get "/v1/runs/${run_id}")"
		status="$(printf '%s' "$body" | jq -r '.status')"
		case "$status" in
			done)
				printf '%s\n' "$body"
				return 0
				;;
			failed|cancelled)
				err="$(printf '%s' "$body" | jq -r '.error // ""')"
				fail "run ${run_id} ended as ${status}: ${err}"
				;;
		esac
		sleep 1
	done
	fail "run ${run_id} did not finish within ${RUN_TIMEOUT_SECONDS}s"
}

cleanup() {
	set +e
	log "cleanup ${topology}"
	local body run_id
	body="$(curl_json POST "/v1/topologies/${topology}/destroy")"
	if [[ "$(printf '%s' "$body" | tail -n1)" == "202" ]]; then
		run_id="$(printf '%s' "$body" | sed '$d' | jq -r '.run_id // empty')"
		if [[ -n "$run_id" ]]; then
			wait_run "$run_id" >/dev/null 2>&1
		fi
	fi
	curl_json DELETE "/v1/topologies/${topology}" >/dev/null 2>&1
}
trap cleanup EXIT

require_cmd curl
require_cmd jq

log "checking API health at ${API_URL}"
api_get /v1/health | jq -e '.status == "ok"' >/dev/null

log "checking Docker agent ${AGENT_ID}"
api_get /v1/agents | jq -e --arg id "$AGENT_ID" '
	.agents[]
	| select(.id == $id and .status == "online" and (.capabilities | index("docker")))
' >/dev/null || fail "agent ${AGENT_ID} is not online with docker capability; run: make api deploy-full"

hcl="$(cat <<HCL
substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "${network_name}" {
  cidr = "172.31.20.0/24"
  nat  = true
}

resource "sysbox_image" "${image_name}" {
  substrate  = substrate.docker.local
  docker_ref = "nginx:alpine"
}

resource "sysbox_node" "${node_name}" {
  substrate = substrate.docker.local
  image     = sysbox_image.${image_name}.id

  link {
    network = sysbox_network.${network_name}.id
    ip      = "172.31.20.10/24"
  }
}

output "web_ip" {
  value = "172.31.20.10"
}
HCL
)"

log "creating topology ${topology}"
payload="$(jq -n --arg name "$topology" --arg hcl "$hcl" '{name:$name,hcl:$hcl}')"
api_post /v1/topologies "$payload" 201 | jq -e --arg name "$topology" '.name == $name' >/dev/null

log "creating revision and plan"
revision="$(api_post "/v1/topologies/${topology}/revisions" "" 201 | jq -r '.id')"
plan="$(api_post "/v1/topologies/${topology}/plans" "" 201)"
plan_id="$(printf '%s' "$plan" | jq -r '.id')"
printf '%s' "$plan" | jq -e '.actions | length >= 3' >/dev/null

log "applying plan ${plan_id} from revision ${revision}"
apply_payload="$(jq -n --arg plan_id "$plan_id" '{plan_id:$plan_id}')"
apply_run="$(api_post "/v1/topologies/${topology}/apply" "$apply_payload" 202)"
apply_run_id="$(printf '%s' "$apply_run" | jq -r '.run_id')"
printf '%s' "$apply_run" | jq -e --arg id "$AGENT_ID" '.agent_id == $id' >/dev/null
wait_run "$apply_run_id" | jq -e '.status == "done"' >/dev/null

log "checking outputs, state, resources, and topology list"
api_get "/v1/topologies/${topology}/outputs" | jq -e '.outputs.web_ip.value == "172.31.20.10"' >/dev/null
api_get "/v1/topologies/${topology}/state" | jq -e --arg node "$node_name" '
	.resources | any(.type == "sysbox_node" and .name == $node)
' >/dev/null
api_get "/v1/topologies/${topology}/resources" | jq -e --arg node "$node_name" '
	.resources | any((.type == "sysbox_node" and .name == $node) or .resource == ("sysbox_node." + $node))
' >/dev/null
api_get /v1/topologies | jq -e --arg name "$topology" '
	.topologies | any(.name == $name and .has_hcl == true and .has_state == true)
' >/dev/null

if command -v docker >/dev/null 2>&1; then
	log "checking host Docker container"
	docker ps --filter "name=sysbox-${node_name}" --format '{{.Names}}' | grep -qx "sysbox-${node_name}" || fail "container sysbox-${node_name} not found"
fi

log "executing command in node ${node_name} through agent console"
session_payload="$(jq -n '{cmd:["/bin/sh","-c","printf sysbox-console-ok"], tty:false, timeout_seconds:90, requested_by:"e2e", roles:["admin"]}')"
session="$(api_post "/v1/topologies/${topology}/nodes/${node_name}/sessions" "$session_payload" 202)"
session_id="$(printf '%s' "$session" | jq -r '.id')"
console_out="$(go run ./tests/e2e/console_client.go -api "$API_URL" -token "$TOKEN" -session "$session_id" -expect "sysbox-console-ok" -timeout 90s)"
printf '%s' "$console_out" | grep -q "sysbox-console-ok" || fail "unexpected console output: ${console_out}"
api_get "/v1/sessions/${session_id}" | jq -e '.status == "closed" or .status == "running"' >/dev/null

log "destroying topology ${topology}"
destroy_run="$(api_post "/v1/topologies/${topology}/destroy" "" 202)"
destroy_run_id="$(printf '%s' "$destroy_run" | jq -r '.run_id')"
wait_run "$destroy_run_id" | jq -e '.status == "done"' >/dev/null
api_get "/v1/topologies/${topology}/state" | jq -e '.resources | length == 0' >/dev/null

if command -v docker >/dev/null 2>&1; then
	if docker ps -a --filter "name=sysbox-${node_name}" --format '{{.Names}}' | grep -qx "sysbox-${node_name}"; then
		fail "container sysbox-${node_name} still exists after destroy"
	fi
fi

log "deleting topology workspace"
api_delete "/v1/topologies/${topology}" | jq -e --arg name "$topology" '.name == $name' >/dev/null
trap - EXIT

log "PASS api smoke: topology=${topology} apply_run=${apply_run_id} destroy_run=${destroy_run_id}"
