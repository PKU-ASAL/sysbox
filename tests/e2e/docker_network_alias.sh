#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/docker-network-alias/field.sysbox.hcl"
state="${root}/.sysbox/e2e/docker-network-alias/state.json"
binary="/tmp/sysbox-docker-network-alias"
prefix="sysbox-lab-docker-network-alias"
network="${prefix}-net-app"
mongo="${prefix}-node-mongo"
target="${prefix}-node-target"
attacker="${prefix}-node-attacker"

cleanup() {
  if [[ -x "${binary}" && -f "${state}" ]]; then
    "${binary}" --state "${state}" -f "${config}" destroy --auto-approve >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

cd "${root}"
GOCACHE="${GOCACHE:-/tmp/sysbox-gocache}" go build -buildvcs=false -o "${binary}" ./cmd/sysbox

"${binary}" --state "${state}" -f "${config}" validate
"${binary}" --state "${state}" -f "${config}" apply --auto-approve

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
"${binary}" --state "${state}" -f "${config}" reset --auto-approve
assert_dns

plan_output="$("${binary}" --state "${state}" -f "${config}" plan)"
grep -q '0 to add, 0 to replace, 0 to destroy, 5 unchanged' <<<"${plan_output}"

"${binary}" --state "${state}" -f "${config}" destroy --auto-approve
trap - EXIT INT TERM

for container in "${mongo}" "${target}" "${attacker}"; do
  ! docker inspect "${container}" >/dev/null 2>&1
done
! docker network inspect "${network}" >/dev/null 2>&1

echo "Docker network alias acceptance passed."
