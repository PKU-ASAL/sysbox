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

Import is handler-owned. The handler reads the external object through an
`Import` capability, normalizes public and opaque state, and returns a resource.
The caller checks mutation safety and saves through `state.Manager`, which uses
locking and compare-and-swap for versioned backends. Imported resources without
a desired payload plan as `NoOp` until configuration explicitly changes them.

`pkg/substrate` contains neutral wire and execution data types only. It has no
lifecycle interface, registry, or driver selection responsibility.
