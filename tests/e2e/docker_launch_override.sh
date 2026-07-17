#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="${root}/tests/e2e/docker-launch/field.sysbox.hcl"
state="${root}/.sysbox/e2e/docker-launch/state.json"
binary="/tmp/sysbox-docker-launch"
service_container="sysbox-lab-docker-launch-node-service"
inherited_container="sysbox-lab-docker-launch-node-inherited"
both_container="sysbox-lab-docker-launch-node-both"
idle_container="sysbox-lab-docker-launch-node-idle"

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

assert_launch_modes() {
  [[ "$(docker exec "${service_container}" cat /tmp/launch-mode)" == "override" ]]
  [[ "$(docker exec "${inherited_container}" cat /tmp/launch-mode)" == "default" ]]
  [[ "$(docker exec "${both_container}" cat /tmp/launch-mode)" == "both" ]]
  docker exec "${idle_container}" test ! -e /tmp/launch-mode
}

assert_launch_modes

"${binary}" --state "${state}" -f "${config}" reset --auto-approve
assert_launch_modes

plan_output="$("${binary}" --state "${state}" -f "${config}" plan)"
grep -q '0 to add, 0 to replace, 0 to destroy, 6 unchanged' <<<"${plan_output}"

"${binary}" --state "${state}" -f "${config}" destroy --auto-approve
trap - EXIT INT TERM
for container in "${service_container}" "${inherited_container}" "${both_container}" "${idle_container}"; do
  ! docker inspect "${container}" >/dev/null 2>&1
done

echo "Docker launch override acceptance passed."
