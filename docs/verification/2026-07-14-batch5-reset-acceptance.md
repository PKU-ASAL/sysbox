# Batch 5 Guest Image and Reset Acceptance

Date: 2026-07-14

## Scope

This acceptance covers the destructive state v6 upgrade, explicit guest family and immutable artifact identity, structured guest execution, and provider-owned reset for Docker, Firecracker, and libvirt. Networking is IPv4-only in this batch; address-family contracts retain an IPv6 extension point.

## Heterogeneous Reset Evidence

Command:

```sh
make test-heterogeneous-reset
```

The isolated Docker runner completed all of the following against one topology containing Docker, Firecracker, and libvirt nodes:

- Initial six-direction communication matrix.
- Three consecutive full-topology resets.
- A targeted reset of each provider node.
- Marker removal from every replacement guest.
- Replacement external identity on every targeted node.
- Stable declared MAC, IPv4 address, and immutable image digest across resets.
- Unchanged external identities for non-target nodes during targeted reset.
- Six-direction communication after every full and targeted reset.
- Final destroy with zero topology-owned container, domain, overlay, rootfs, process, socket, bridge, veth, and namespace residue.

Result:

```text
Heterogeneous reset acceptance passed: 3 full cycles, 3 targeted resets, zero owned residue.
```

## Ownership and Recovery Hardening

- Runtime checkpoints the provider reset handle before destructive work and after apply, NIC wiring, and node start.
- Docker deletes only the container identified by its persisted ownership anchor.
- libvirt requires the exact persisted domain UUID and owned overlay path from structured domain XML.
- libvirt VM directories carry a domain-and-UUID ownership manifest that is revalidated before recursive deletion.
- Firecracker requires generation-specific PID, process start time, VM identity, and socket anchors.
- A repeated destroy treats a missing old generation as converged only when the exact persisted process is also gone.
- Immutable baselines are rehashed before replacement creation.
- Resume verifies the reset plan fingerprint and recovers idempotently from durable checkpoints.
- State switches to the replacement only after observation, refresh, cleanup, and final patch succeed.
- State patches persist the provider-neutral external ID directly; reset does not introduce Docker-specific runtime keys.

## Protected Host Resources

The acceptance uses isolated, topology-scoped names. After completion, the pre-existing `mixed`, `recon`, and `docker-service` Docker resources, the `env_attacker`, `env_mgr`, and `env_node-a` libvirt domains, and the `sysbox-net-lab-mixed-net-net_internal` and `sysbox-net-lab-mixed-net-net_dmz` namespaces remained present.

## Final Gates

The final commit is gated on:

```sh
go test ./...
go vet ./...
CGO_ENABLED=1 go test -race ./pkg/runtime ./pkg/state ./pkg/provider/docker ./pkg/provider/libvirt ./pkg/provider/firecracker
make ci
git diff --check
```

The removal audit also rejects production legacy execution APIs, inline provisioner commands, legacy public image fields, state migration or compatibility readers, provider-name branching in runtime reset orchestration, and serialized reset secrets.

All final gates passed after the ownership and recovery review fixes. A complete heterogeneous reset rerun also passed after those fixes. Sandboxed race/CI attempts could not bind the loopback sockets used by `httptest`; the identical commands passed outside that restricted network sandbox.
