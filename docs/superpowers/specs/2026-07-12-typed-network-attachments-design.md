# Typed Network Attachments Design

## Scope

This design is the first independently deliverable part of Batch 4. It replaces
untyped node and router NIC state with stable logical attachments shared by
Docker, libvirt, and Firecracker. Atomic nftables policy, controlled egress,
and final ownership residue scans remain later Batch 4 projects.

## Compatibility

Persistent state schema increases from v4 to v5. Loading v1-v4 state fails
before mutation with an error that reports the found and required versions and
instructs the user to remove the old state or rebuild the topology. There is no
migration, compatibility decoder, dual write, or automatic destroy of objects
described by incompatible state.

The HCL change is also intentional and immediate. Every node `link` and router
`interface` has an explicit logical name. Examples, fixtures, and documentation
move to the new syntax in the same implementation series.

```hcl
link "uplink" {
  network = sysbox_network.public
  ip      = "10.0.0.10/24"
}
```

## Attachment Model

`state.Attachment` is typed persistent state owned by core. A node or router
resource stores attachments as a proper resource field rather than under
`Attributes["nics"]`.

An attachment contains:

- logical name;
- node and network resource addresses;
- stable MAC address;
- zero or more IP prefixes;
- optional gateway;
- driver kind;
- the latest observed guest device name, when the driver can report it;
- opaque driver state encoded and decoded only by that driver capability.

Attachment identity is `(node address, logical name)`. Logical names are
required, valid identifiers, and unique within a node or router. Guest device
names such as `eth1`, host veth names, tap names, namespace names, Docker
network IDs, and libvirt device IDs are not semantic identity.

When HCL does not specify a MAC, Sysbox deterministically derives a locally
administered unicast MAC from topology identity, node address, and logical
attachment name. An explicit valid unicast MAC overrides the derived value.
Renaming an attachment therefore deletes the old attachment and creates a new
one rather than silently transferring identity.

## Core And Driver Boundary

Node links and router interfaces decode to one attachment intent type. Plan
resolves network references, validates names, MACs, prefixes, gateways, and
duplicates, then includes the normalized intent in typed diffing.

The NIC capability receives the logical identity and normalized semantic
request. It chooses concrete device names and mechanisms. It returns semantic
observations plus opaque state required for later observe, delete, and recovery
operations. Runtime does not assign `ethN`, parse opaque state, or infer
attachment identity from declaration order.

Routes, NAT, and later firewall configuration refer only to logical attachment
names. At an execution boundary, the responsible capability resolves that name
against current observations. A changed concrete device name alone updates the
observation and does not cause replacement.

## Apply, Refresh, And Recovery

Apply checkpoints each attachment by `(node address, logical name)`. After an
interruption, recovery observes the attachment before deciding whether to
adopt it, retry creation, or return `invalid-state`. This prevents duplicate
veth, tap, or managed-network attachment operations.

Refresh observes attachments independently:

- matching semantic state is `present`;
- an absent external attachment is `drifted` with `not-found` evidence;
- an observation failure is `unknown` and preserves durable state;
- a concrete device-name change with matching logical identity, MAC, prefixes,
  gateway, and network updates observation without replacement.

Driver failures use existing stable categories. `unsupported` combinations
fail during plan preflight. `unavailable` and `permission-denied` stop apply or
refresh without rewriting known state. Opaque state decode failures are
`invalid-state`.

Destroy enumerates typed attachments and asks the owning capability to delete
each one before deleting the node or router. The capability owns interpretation
of opaque state. Full topology ownership scans and residue inventory are part
of the later Batch 4 cleanup project.

## Validation And Errors

Configuration decoding and planning reject:

- unnamed or duplicate logical attachments;
- invalid or multicast explicit MAC addresses;
- invalid or duplicate IP prefixes within an attachment;
- gateways outside every configured attachment prefix;
- missing network resource references;
- route, NAT, or firewall references to unknown logical names;
- drivers missing required attach, observe, delete, or state-codec capability.

Errors identify the node address and logical attachment name. Validation and
preflight happen before external mutation.

## Verification

The implementation is divided into atomic TDD tasks and covers:

- rejection of v4 state and deterministic v5 round trips;
- explicit and unique logical names for node and router attachments;
- deterministic default MACs and validated overrides;
- normalized typed state across Docker, libvirt, and Firecracker;
- device-name observation changes without semantic replacement;
- absent versus failed observations;
- idempotent recovery after an interrupted attachment operation;
- removal of `Attributes["nics"]`, runtime `ethN` assignment, and semantic
  consumers of host or guest implementation names.

Each task runs focused RED/GREEN tests, the full Go suite, `go vet`, relevant
race tests, and removal searches before an atomic commit.

## Acceptance

- v4 and older state is rejected without mutation; new state is v5 only.
- Every node link and router interface has an explicit logical name.
- Core persists typed attachment semantics and never parses driver opaque state.
- Docker, libvirt, and Firecracker use the same attachment identity and state
  model without provider-specific HCL interface names.
- Refresh and interrupted recovery do not duplicate attachments or replace an
  attachment solely because a concrete device name changed.
- No runtime behavior reads or writes legacy untyped `nics` attributes.
