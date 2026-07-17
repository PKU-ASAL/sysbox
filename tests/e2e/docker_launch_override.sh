#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/docker-launch/field.sysbox.hcl"
state="${root}/.sysbox/e2e/docker-launch/state.json"
binary="/tmp/sysbox-docker-launch"
container="sysbox-lab-docker-launch-node-service"

cleanup() {
  if [[ -x "${binary}" && -f "${state}" ]]; then
    "${binary}" --state "${state}" -f "${config}" destroy --auto-approve >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

cd "${root}"

docker build -t sysbox-launch-override:test "${root}/tests/e2e/docker-launch"
GOCACHE="${GOCACHE:-/tmp/sysbox-gocache}" go build -buildvcs=false -o "${binary}" ./cmd/sysbox

"${binary}" --state "${state}" -f "${config}" validate
"${binary}" --state "${state}" -f "${config}" apply --auto-approve
[[ "$(docker exec "${container}" cat /tmp/launch-mode)" == "override" ]]

"${binary}" --state "${state}" -f "${config}" reset --auto-approve
[[ "$(docker exec "${container}" cat /tmp/launch-mode)" == "override" ]]

plan_output="$("${binary}" --state "${state}" -f "${config}" plan)"
grep -q '0 to add, 0 to replace, 0 to destroy, 3 unchanged' <<<"${plan_output}"

"${binary}" --state "${state}" -f "${config}" destroy --auto-approve
trap - EXIT INT TERM
! docker inspect "${container}" >/dev/null 2>&1

echo "Docker launch override acceptance passed."
