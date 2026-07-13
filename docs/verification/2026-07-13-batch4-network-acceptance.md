# Batch 4 Network Acceptance Evidence

Date: 2026-07-13

## Environment

- Host user: non-root, member of `docker`, `libvirt`, and `kvm` groups.
- Docker Engine: 27.5.1.
- Firecracker binary and cached vmlinux/rootfs: present.
- Libvirt: reachable; the Ubuntu 22.04 Vagrant qcow2 base is present at
  `/var/lib/libvirt/images/generic-VAGRANTSLASH-ubuntu2204_vagrant_box_image_4.3.12_box.img`.
- Privileged evidence ran offline in the existing `golang:1.26-alpine` image.

## Passed Evidence

`make test-privileged-container` passed these real kernel/provider tests:

- owned nftables first apply, readback, repeated atomic replacement, stable
  semantic digest, stable inventory, masquerade chain, delete, and not-found
  verification;
- actual TCP input/output default deny, explicit destination-port allow, and
  established/related return traffic;
- actual three-namespace source-to-router-to-destination forwarding: default
  drop blocks the connection, while logical inside/uplink rules allow only the
  declared new flow and stateful return path;
- Docker provider container creation, PID/netns resolution, owned nftables
  apply/observe/reapply/delete, stable digest, masquerade inventory, and
  container cleanup;
- interrupted local-network checkpoint recovery, repeated recovery idempotency,
  and namespace cleanup;
- Firecracker attachment checkpoint recovery, repeated recovery idempotency,
  tap cleanup, VM directory cleanup, and preservation of the separately-owned
  shared network namespace.

Expression readback signatures are calculated from actual netlink expressions.
Dynamic counter packet/byte values are excluded, so traffic does not create
false drift while matcher or verdict changes do.

The `examples/controlled-egress` fixture also passes validate/plan and is part
of `make ci`.

## Standard Gates

The following passed after implementation:

- `go test ./...`
- `go vet ./...`
- focused `go test -race`
- `make ci`, including all six topology plans
- privileged build-tag compilation
- legacy removal audit
- `git diff --check`

## Heterogeneous Matrix Evidence

`examples/heterogeneous-matrix` plans as eight resources and repeatedly reached
`Apply complete` with Docker, Firecracker, and libvirt domains attached to the
same declared IPv4 network. The Linux network driver creates an owned root-netns
libvirt bridge joined by an owned veth transit to the isolated network bridge;
this removed the system libvirt daemon's namespace visibility failure. Docker
to Firecracker ICMP (`10.44.0.20`) passed immediately through that topology.

Full Docker/Firecracker/libvirt guest-to-guest communication remains unproven
with the available qcow2. The generated NoCloud seed contains the declared MAC
and `10.44.0.30/24`, the libvirt domain starts, and its vnet is attached to the
owned root bridge, but the Vagrant guest never claims `10.44.0.30`. A temporary
explicit DHCP bootstrap experiment obtained a `192.168.122.x` lease but the
guest SSH service never became reachable and the lease subsequently stopped
responding. The experiment was removed from the implementation because it did
not meet the acceptance contract.

Every failed or diagnostic apply ran under a destroy trap. Post-run audits
found no heterogeneous-matrix Docker container, libvirt domain, named network
namespace, root bridge, transit veth, tap, or Firecracker process residue. The
pre-existing `mixed`, `recon`, `docker-service`, and `env_*` resources were not
modified.

Do not report the full heterogeneous guest communication matrix as passed until
a cloud-init-capable libvirt image claims the declared static address and the
runner records communication, an unchanged repeated plan, destroy, and zero
residue.
