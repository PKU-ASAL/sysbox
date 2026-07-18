# Docker Managed-Network Alias Design

## Goal

Give Docker nodes stable topology-local DNS names on Sysbox-managed Docker
networks so migrated containerlab and Docker Compose workloads can keep using
logical node names such as `mongo` and `target` instead of fixed IP addresses.

## Scope

This change applies only to Docker nodes attached to `nat = true` networks,
which are created and connected through the Docker Engine and therefore have
Docker embedded DNS.

It provides:

1. an automatic alias equal to the `sysbox_node` resource name on every
   Docker-managed network attachment; and
2. optional extra aliases declared on a node `link` block.

It does not add a container hostname field, inject `/etc/hosts`, run a DNS
server, or claim name resolution for isolated netns/veth networks,
Firecracker, libvirt, routers, or imported unmanaged networks.

## HCL Contract

The common node declaration remains concise:

```hcl
resource "sysbox_node" "mongo" {
  substrate = substrate.docker.local
  image     = sysbox_image.mongo.id

  link "lab" {
    network = sysbox_network.lab.id
    ip      = "10.77.1.10/24"
  }
}
```

When `sysbox_network.lab` has `nat = true`, Docker registers `mongo` as the
endpoint alias without another HCL attribute.

Additional service names are optional:

```hcl
link "lab" {
  network = sysbox_network.lab.id
  ip      = "10.77.1.10/24"
  aliases = ["mongodb", "database"]
}
```

The effective alias list is the node resource name followed by the declared
aliases, with duplicates removed while preserving first occurrence. Empty or
whitespace-containing aliases are rejected. Duplicate aliases across different
nodes are allowed because Docker intentionally supports one service alias
resolving to multiple endpoints.

`aliases` is accepted only on `sysbox_node.link`. Router interfaces do not gain
aliases in this change.

## Attachment Model

Aliases are desired attachment semantics, not Docker-private incidental state.
They flow through the existing typed network path:

```text
LinkConfig
  -> AttachmentInput
  -> AttachmentIntent
  -> NICSpec
  -> driver.AttachmentRequest
  -> state.Attachment
```

The Docker NIC driver passes effective aliases to
`network.EndpointSettings.Aliases` when the target network state is
Docker-managed. The driver ignores no aliases silently: if aliases reach a
Docker attachment whose network is not Docker-managed, attach fails with a
clear unsupported error. Runtime does not send the automatic or explicit alias
list to Firecracker, libvirt, router interfaces, or isolated Docker veth
attachments.

Aliases are persisted in `state.Attachment` and reconstructed for refresh,
checkpoint recovery, reset deletion, and reset reattachment. Observation of a
Docker-managed attachment compares the desired aliases with the endpoint's
observed aliases. Missing aliases report attachment drift instead of healthy.

## Planning and Replacement

The effective alias list participates in the node desired payload. Adding,
removing, or reordering aliases requires node replacement under the current
immutable attachment model.

The automatic logical-name alias is also included in desired configuration.
Consequently, nodes created by a release before this feature plan replacement
when first evaluated by the new release. This is intentional; no compatibility
shim or silent in-place mutation is provided.

Stored plans continue to bind driver and resource schema versions. The node
resource schema version is incremented so a plan created before the alias
contract cannot be applied after upgrading.

## Naming Boundary

The automatic alias is the HCL resource label, not the topology-prefixed
external container name. For example:

```text
logical address:  sysbox_node.mongo
DNS alias:        mongo
external object:  sysbox-lab-cve-2019-10758-node-mongo
```

Sysbox does not set Docker `Config.Hostname`. Container internal hostname and
network service discovery remain separate concepts.

## Reset and Recovery

Reset rebuilds attachment intents from current HCL, recreates the container,
and reconnects each Docker-managed network with the same effective aliases.
Checkpoint state carries aliases so interrupted cleanup and recovery use the
same declared identity.

Recovery may adopt a matching Docker endpoint only when its effective alias set
matches desired state. Otherwise the resource is recovered as drifted and the
next plan requires replacement.

## Validation and Testing

Unit tests cover:

- automatic node-name alias generation;
- declared extra aliases and stable de-duplication;
- rejection of empty and whitespace-containing aliases;
- aliases in desired diff and durable attachment state;
- propagation through create, refresh, checkpoint, and reset request paths;
- Docker `NetworkConnect` receiving exact endpoint aliases;
- Docker observation detecting missing aliases;
- rejection when aliases are used on isolated veth attachments; and
- absence of aliases on routers, Firecracker, and libvirt attachment requests.

A real Docker acceptance creates `mongo`, `target`, and `attacker` nodes on one
managed network and proves:

```text
target -> mongo
attacker -> target
attacker -> an explicit extra alias
```

The acceptance repeats the checks after whole-topology reset, verifies a no-op
plan, destroys the topology, and confirms no owned containers or networks
remain.

## Completion Criteria

The change is complete when:

1. Docker-managed network peers resolve every node by its logical HCL name;
2. extra aliases behave deterministically and survive reset/recovery;
3. unsupported network/provider combinations fail before partial attachment;
4. alias drift is observable;
5. VM and isolated-network behavior is unchanged; and
6. focused, full, and real Docker acceptance tests pass.
