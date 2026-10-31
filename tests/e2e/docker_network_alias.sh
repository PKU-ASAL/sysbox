#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/docker-network-alias/field.sysbox.hcl"
binary="${SYSBOX_E2E_BINARY:?set the verified sysbox binary path}"
: "${SYSBOX_API_URL:?set the isolated API URL}"
: "${SYSBOX_E2E_AGENT_ID:?set the isolated execution host Agent ID}"
topology="sysbox-alias-${RANDOM}-${RANDOM}"
# Run on the isolated execution host; provider probes use its Docker daemon.
export SYSBOX_API_TOKEN="${SYSBOX_API_TOKEN:?API token required}"
prefix="sysbox-lab-${topology}"
network="${prefix}-net-app"
mongo="${prefix}-node-mongo"
target="${prefix}-node-target"
attacker="${prefix}-node-attacker"

trap 'echo "Acceptance interrupted; retain topology ${topology} and inspect its Runs before cleanup" >&2' ERR

cd "${root}"
source "${root}/tests/e2e/service_host_check.sh"

"${binary}" -f "${config}" validate
"${binary}" --topology "${topology}" -f "${config}" apply --auto-approve

assert_dns() {
  local peer
  for peer in "${target}" "${attacker}"; do
    docker exec "${peer}" getent hosts mongo >/dev/null
    docker exec "${peer}" getent hosts target >/dev/null
    docker exec "${peer}" getent hosts database >/dev/null
    docker exec "${peer}" ping -c 1 -W 2 mongo >/dev/null
    docker exec "${peer}" ping -c 1 -W 2 database >/dev/null
  done
}

assert_dns
"${binary}" --topology "${topology}" -f "${config}" reset --auto-approve
assert_dns

plan_output="$("${binary}" --topology "${topology}" -f "${config}" plan)"
jq -e 'all(.actions[]; .action == "no-op")' <<<"${plan_output}" >/dev/null

"${binary}" --topology "${topology}" destroy --auto-approve
trap - ERR

for container in "${mongo}" "${target}" "${attacker}"; do
  ! docker inspect "${container}" >/dev/null 2>&1
done
! docker network inspect "${network}" >/dev/null 2>&1

echo "Docker network alias acceptance passed."
