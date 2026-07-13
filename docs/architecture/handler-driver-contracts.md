# Resource Handler And Driver Contracts

Resource handlers own resource semantics: schema, graph decoding, validation,
planning, lifecycle normalization, observation, and import normalization.
Handlers do not execute host infrastructure commands or import concrete driver
packages.

Capability drivers own external operations. A driver descriptor declares only
the capabilities it implements. Consumers request capabilities through
`driver.Registry`; missing drivers and unsupported combinations are stable
categorized errors. Planning validates handler requirements before producing a
mutation plan.

Node state is persisted as public typed attributes plus an opaque provider
state envelope. `NodeState` is the only capability allowed to encode or decode
that envelope. Durable state, plans, and operation logs must not contain
resolved secrets.

Network attachment identity is `(owner resource address, logical name)` and is
persisted in `state.Resource.Attachments`. Core owns the network address, MAC,
IP prefixes, gateway, and latest observation. The NIC capability exclusively
owns its opaque attachment state and implements attach, observe, and delete.
Runtime never assigns or interprets guest `ethN`, veth, tap, namespace, Docker
endpoint, or libvirt device names.

Router NAT and `sysbox_firewall` use the typed `Policy` capability. Core owns
IPv4 policy semantics, logical attachment references, deterministic ownership,
and desired digests. The provider resolves current physical devices and applies
one topology-owned nftables table atomically. Apply succeeds only after
readback; refresh compares the observed digest; destroy verifies the full owner
marker before deletion and reports residue. Runtime never executes `iptables`,
`nft`, or `nsenter` and never persists nftables handles or physical devices.

Policy is IPv4-only. IPv6 policy input fails validation explicitly, while the
driver contract carries an address family for a future IPv6 compiler. The old
fixed `sysbox_fw`, append-style iptables rules, and `RouterNetwork` capability
have no compatibility path. `sysbox_firewall.attach_to` must reference a node
or router; rules use explicit direction, verdict, logical interfaces, CIDRs,
port ranges, protocol, connection states, counters, and rate-limited logging.

State schema v5 is a hard break. Older state is rejected without mutation or
migration. Operation checkpoints persist typed attachments and resource private
state so recovery can observe before adopting; a missing attachment is adopted
as drifted for controlled replacement, while unavailable or invalid state stops
recovery.

Import is handler-owned. The handler reads the external object through an
`Import` capability, normalizes public and opaque state, and returns a resource.
The caller checks mutation safety and saves through `state.Manager`, which uses
locking and compare-and-swap for versioned backends. Imported resources without
a desired payload plan as `NoOp` until configuration explicitly changes them.

`pkg/substrate` contains neutral wire and execution data types only. It has no
lifecycle interface, registry, or driver selection responsibility.
