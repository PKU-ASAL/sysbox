#!/usr/bin/env sh
set -eu

state=/tmp/sysbox-e2e/heterogeneous-matrix/state.json
config=examples/heterogeneous-matrix/field.sysbox.hcl
docker_node=sysbox-lab-heterogeneous-matrix-node-docker
marker=/tmp/sysbox-batch5-reset-marker
mkdir -p "$(dirname "${state}")" bin

cleanup() {
  find /var/lib/sysbox/firecracker -name firecracker.log -type f -exec sh -c 'echo "--- $1 ---"; tail -80 "$1"' _ {} \; 2>/dev/null || true
  if [ -f "${state}" ]; then
    ./bin/sysbox --state "${state}" -f "${config}" destroy --auto-approve || true
  fi
}
trap cleanup EXIT INT TERM

CGO_ENABLED=0 go build -buildvcs=false -o bin/sysbox ./cmd/sysbox
./bin/sysbox --state "${state}" -f "${config}" apply --auto-approve
netns="$(go run ./tests/e2e/matrixprobe -state "${state}" -query netns)"

query() { go run ./tests/e2e/matrixprobe -state "${state}" -query "$1"; }
node_id() { query "node_id:$1"; }

assert_identity_stable() {
  [ "$(query node_link:docker)" = "${docker_link}" ]
  [ "$(query node_link:firecracker)" = "${firecracker_link}" ]
  [ "$(query node_link:libvirt)" = "${libvirt_link}" ]
  [ "$(query image_digest:docker)" = "${docker_digest}" ]
  [ "$(query image_digest:firecracker)" = "${firecracker_digest}" ]
  [ "$(query image_digest:libvirt)" = "${libvirt_digest}" ]
}

wait_libvirt() {
  i=0
  until ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=2 sysbox@10.44.0.30 true; do
    i=$((i + 1)); [ "${i}" -lt 60 ] || return 1; sleep 2
  done
}

assert_matrix() {
  docker exec "${docker_node}" ping -c 2 -W 2 10.44.0.20
  docker exec "${docker_node}" ping -c 2 -W 2 10.44.0.30
  go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -target 10.44.0.10
  go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -target 10.44.0.30
  wait_libvirt
  ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 'ping -c 2 -W 2 10.44.0.10'
  ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 'ping -c 2 -W 2 10.44.0.20'
}

write_markers() {
  docker exec "${docker_node}" touch "${marker}"
  go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -touch "${marker}"
  ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 "touch ${marker}"
}

assert_markers_absent() {
  docker exec "${docker_node}" test ! -e "${marker}"
  i=0
  until go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -check-absent "${marker}"; do
    i=$((i + 1)); [ "${i}" -lt 30 ] || return 1; sleep 1
  done
  wait_libvirt
  ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 "test ! -e ${marker}"
}

docker_link="$(query node_link:docker)"
firecracker_link="$(query node_link:firecracker)"
libvirt_link="$(query node_link:libvirt)"
docker_digest="$(query image_digest:docker)"
firecracker_digest="$(query image_digest:firecracker)"
libvirt_digest="$(query image_digest:libvirt)"
assert_matrix

cycle=1
while [ "${cycle}" -le 3 ]; do
  old_docker="$(node_id docker)"; old_firecracker="$(node_id firecracker)"; old_libvirt="$(node_id libvirt)"
  write_markers
  ./bin/sysbox --state "${state}" -f "${config}" reset --auto-approve
  [ "$(node_id docker)" != "${old_docker}" ]
  [ "$(node_id firecracker)" != "${old_firecracker}" ]
  [ "$(node_id libvirt)" != "${old_libvirt}" ]
  assert_markers_absent
  assert_identity_stable
  assert_matrix
  cycle=$((cycle + 1))
done

for target in docker firecracker libvirt; do
  before_docker="$(node_id docker)"; before_firecracker="$(node_id firecracker)"; before_libvirt="$(node_id libvirt)"
  ./bin/sysbox --state "${state}" -f "${config}" reset --target "sysbox_node.${target}" --auto-approve
  for node in docker firecracker libvirt; do
    before="$(eval "printf '%s' \"\${before_${node}}\"")"
    after="$(node_id "${node}")"
    if [ "${node}" = "${target}" ]; then [ "${after}" != "${before}" ]; else [ "${after}" = "${before}" ]; fi
  done
  assert_identity_stable
  assert_matrix
done

./bin/sysbox --state "${state}" -f "${config}" destroy --auto-approve
trap - EXIT INT TERM
! docker inspect "${docker_node}" >/dev/null 2>&1
! virsh dominfo sysbox-lab-heterogeneous-matrix-node-libvirt >/dev/null 2>&1
! pgrep -af firecracker | grep -F 'sysbox-lab-heterogeneous-matrix-node-firecracker' >/dev/null 2>&1
rm -rf /tmp/sysbox-e2e/heterogeneous-matrix
rm -f "${SYSBOX_QCOW2}"
echo "Heterogeneous reset acceptance passed: 3 full cycles, 3 targeted resets, zero owned residue."
