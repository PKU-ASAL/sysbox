# Typed State V4

State v4 stores canonical addresses, resource and schema identity, external ID,
typed public attributes, dependencies, explicit observation status, and UTC
timestamps. Driver details use a versioned opaque `private` envelope.

State v3 and older are rejected without mutation. There is no migration path:
destroy the lab with the binary that created it, then recreate it with the
current binary.

Observation status is one of `present`, `absent`, `drifted`, `degraded`, or
`unknown`. Unknown observations block apply; they never trigger replacement.
Dependency changes are planned by each dependent resource schema and do not
cause automatic replacement cascades.
