#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/docker-launch/field.sysbox.hcl"
binary="${SYSBOX_E2E_BINARY:?set the verified sysbox binary path}"
: "${SYSBOX_API_URL:?set the isolated API URL}"
: "${SYSBOX_E2E_AGENT_ID:?set the isolated execution host Agent ID}"
topology="sysbox-launch-${RANDOM}-${RANDOM}"
# Run on the isolated execution host; provider probes use its Docker daemon.
export SYSBOX_API_TOKEN="${SYSBOX_API_TOKEN:?API token required}"
service_container="sysbox-lab-${topology}-node-service"
inherited_container="sysbox-lab-${topology}-node-inherited"
both_container="sysbox-lab-${topology}-node-both"
idle_container="sysbox-lab-${topology}-node-idle"

trap 'echo "Acceptance interrupted; retain topology ${topology} and inspect its Runs before cleanup" >&2' ERR

cd "${root}"
source "${root}/tests/e2e/service_host_check.sh"

docker build -t sysbox-launch-override:test "${root}/tests/e2e/docker-launch"

"${binary}" -f "${config}" validate
"${binary}" --topology "${topology}" -f "${config}" apply --auto-approve

assert_launch_modes() {
  [[ "$(docker exec "${service_container}" cat /tmp/launch-mode)" == "override" ]]
  [[ "$(docker exec "${inherited_container}" cat /tmp/launch-mode)" == "default" ]]
  [[ "$(docker exec "${both_container}" cat /tmp/launch-mode)" == "both" ]]
  docker exec "${idle_container}" test ! -e /tmp/launch-mode
}

assert_launch_modes

"${binary}" --topology "${topology}" -f "${config}" reset --auto-approve
assert_launch_modes

plan_output="$("${binary}" --topology "${topology}" -f "${config}" plan)"
jq -e 'all(.actions[]; .action == "no-op")' <<<"${plan_output}" >/dev/null

"${binary}" --topology "${topology}" destroy --auto-approve
trap - ERR
for container in "${service_container}" "${inherited_container}" "${both_container}" "${idle_container}"; do
  ! docker inspect "${container}" >/dev/null 2>&1
done

echo "Docker launch override acceptance passed."
