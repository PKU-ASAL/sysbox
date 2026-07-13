# Batch 4 Network Acceptance Evidence

Date: 2026-07-13

## Environment

- Host user: non-root, member of `docker`, `libvirt`, and `kvm` groups.
- Docker Engine: 27.5.1.
- Firecracker binary and cached vmlinux/rootfs: present.
- Libvirt: reachable; acceptance uses the official Ubuntu 24.04 cloud image at
  `https://cloud-images.ubuntu.com/releases/noble/release-20260615/ubuntu-24.04-server-cloudimg-amd64.img`,
  pinned to SHA-256
  `5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44`.
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

`make test-heterogeneous-matrix` prepared the digest-pinned image, generated an
ephemeral Ed25519 key, and applied eight resources on one declared IPv4 network.
The libvirt provider's explicit `cloud_init` mode configured `10.44.0.30/24`
through NoCloud. All six directed ICMP edges passed:

- Docker to Firecracker and libvirt;
- Firecracker to Docker and libvirt;
- libvirt to Docker and Firecracker.

The repeated plan reported exactly
`Plan: 0 to add, 0 to replace, 0 to destroy, 8 unchanged.` Destroy then removed
all eight resources. Post-destroy audits found no matrix Docker container,
libvirt domain, named network namespace, root bridge, transit veth, libvirt VM
directory, or Firecracker process residue. The runner also removed its state,
ephemeral key, and writable qcow2 copy. The pre-existing `mixed`, `recon`,
`docker-service`, and `env_*` resources were not modified.
