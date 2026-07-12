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

Router NAT receives logical attachment requests and driver observations. The
router-network capability resolves current physical devices at the execution
boundary. Docker-managed interfaces are resolved by their configured IP; a
concrete device-name change is an observation update, not semantic drift.

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
