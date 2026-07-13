# Batch 4 Network Acceptance Evidence

Date: 2026-07-13

## Environment

- Host user: non-root, member of `docker`, `libvirt`, and `kvm` groups.
- Docker Engine: 27.5.1.
- Firecracker binary and cached vmlinux/rootfs: present.
- Libvirt: reachable; no configured Batch 4 qcow2 base image was found.
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

## Unproven External Matrix

Full Docker/Firecracker/libvirt guest-to-guest communication through one
applied topology remains unproven on this host. The libvirt qcow2 fixture is
missing. A nested privileged-container attempt reached router creation but its
named-network-namespace wiring to a sibling Docker container returned `EINVAL`;
the trap destroyed all resources recorded by that attempt. This nested mount
namespace limitation does not affect the provider-level Docker policy or
three-namespace kernel tests above.

Do not report the heterogeneous guest communication matrix as passed until a
root-capable host with the qcow2 fixture runs it and records zero residue.
