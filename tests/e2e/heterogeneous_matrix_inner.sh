#!/usr/bin/env sh
set -eu

state=/tmp/sysbox-e2e/heterogeneous-matrix/state.json
config=examples/heterogeneous-matrix/field.sysbox.hcl
docker_node=sysbox-lab-heterogeneous-matrix-node-docker
mkdir -p "$(dirname "${state}")" bin

cleanup() {
  if [ -f "${state}" ]; then
    ./bin/sysbox --state "${state}" -f "${config}" destroy --auto-approve || true
  fi
}
trap cleanup EXIT INT TERM

CGO_ENABLED=0 go build -buildvcs=false -o bin/sysbox ./cmd/sysbox
./bin/sysbox --state "${state}" -f "${config}" apply --auto-approve

netns="$(go run ./tests/e2e/matrixprobe -state "${state}" -query netns)"
root_bridge="$(go run ./tests/e2e/matrixprobe -state "${state}" -query libvirt_bridge)"
root_veth="$(go run ./tests/e2e/matrixprobe -state "${state}" -query root_veth)"
libvirt_vm_dir="$(go run ./tests/e2e/matrixprobe -state "${state}" -query libvirt_vm_dir)"

docker exec "${docker_node}" ping -c 3 -W 2 10.44.0.20
docker exec "${docker_node}" ping -c 3 -W 2 10.44.0.30
go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -target 10.44.0.10
go run ./tests/e2e/matrixprobe -state "${state}" -node firecracker -target 10.44.0.30

i=0
until ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=2 sysbox@10.44.0.30 true; do
  i=$((i + 1))
  if [ "${i}" -ge 60 ]; then
    echo "libvirt guest SSH did not become ready" >&2
    exit 1
  fi
  sleep 2
done
ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 'ping -c 3 -W 2 10.44.0.10'
ip netns exec "${netns}" ssh -i "${SYSBOX_MATRIX_SSH_PRIVATE_KEY}" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null sysbox@10.44.0.30 'ping -c 3 -W 2 10.44.0.20'

plan_output="$(./bin/sysbox --state "${state}" -f "${config}" plan)"
printf '%s\n' "${plan_output}"
printf '%s\n' "${plan_output}" | grep -q 'Plan: 0 to add, 0 to replace, 0 to destroy, 8 unchanged.'
! printf '%s\n' "${plan_output}" | grep -q 'desired configuration hash changed'

./bin/sysbox --state "${state}" -f "${config}" destroy --auto-approve
trap - EXIT INT TERM

! docker inspect "${docker_node}" >/dev/null 2>&1
! virsh dominfo sysbox-lab-heterogeneous-matrix-node-libvirt >/dev/null 2>&1
[ ! -e "/run/netns/${netns}" ]
! ip link show "${root_bridge}" >/dev/null 2>&1
! ip link show "${root_veth}" >/dev/null 2>&1
[ ! -e "${libvirt_vm_dir}" ]
! pgrep -af firecracker | grep -F 'sysbox-lab-heterogeneous-matrix-node-firecracker' >/dev/null 2>&1
rm -rf /tmp/sysbox-e2e/heterogeneous-matrix
rm -f "${SYSBOX_QCOW2}"

echo "Heterogeneous matrix acceptance passed with zero owned residue."
