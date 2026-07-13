# Atomic Nftables Policy Design

## Scope

This Batch 4 subproject replaces append-style iptables NAT and the fixed
`sysbox_fw` table with topology-owned, atomically replaced nftables rulesets.
It implements stateful L3/L4 firewall policy, router NAT, controlled egress,
digest-based observation, and ownership-aware cleanup.

The first implementation supports IPv4 only. The contract carries an explicit
address family so IPv6 can be added without changing the runtime/provider
boundary. Configuration containing IPv6 CIDRs is rejected before apply with a
specific unsupported-family diagnostic. No IPv4/IPv6 compatibility path or
partial IPv6 behavior is retained.

Domain filtering is not part of this subsystem. A managed L7 proxy may be an
explicitly allowed destination, but Sysbox does not interpret domain names in
firewall policy.

## Ownership Boundary

Core owns policy semantics. It validates and normalizes rules, resolves
resource addresses and logical attachment names, persists the desired digest,
and classifies observation.

Providers own execution details. They resolve logical attachments to concrete
interfaces, select the network namespace, compile typed policy to nftables
expressions, apply it, read it back, and remove it. Runtime never handles a
container PID, namespace file descriptor, physical interface name, nftables
handle, or provider command.

Linux network and Docker implement the same typed ruleset lifecycle. The Linux
network provider executes in its isolated namespace. Docker executes in the
router container network namespace it owns. A provider may choose a native
library or another structured backend internally, but the initial Linux and
Docker implementations use `github.com/google/nftables` and do not shell out to
`nft`, `iptables`, or `nsenter` for policy mutation.

## Driver Contract

The policy capability exposes three operations:

```go
type AddressFamily string

const FamilyIPv4 AddressFamily = "ipv4"

type RulesetSpec struct {
    Owner         string
    Family        AddressFamily
    DefaultInput  Verdict
    DefaultOutput Verdict
    DefaultForward Verdict
    Rules         []PolicyRule
    NAT           *NATPolicy
}

type RulesetObservation struct {
    Table     string
    Digest    string
    Inventory []OwnedObject
}

type Policy interface {
    ApplyRuleset(context.Context, PolicyTarget, RulesetSpec) (RulesetObservation, error)
    ObserveRuleset(context.Context, PolicyTarget, string) (RulesetObservation, error)
    DeleteRuleset(context.Context, PolicyTarget, string) error
}
```

`PolicyTarget` identifies the owning resource and passes provider-owned node or
network state. It never exposes physical interface identity to runtime.

`ApplyRuleset` is successful only after the provider reads back the installed
table and verifies its canonical digest. `ObserveRuleset` returns stable driver
error categories: not-found, unavailable, invalid-state, and conflict.
`DeleteRuleset` verifies the ownership marker before deletion, deletes only the
owned table, then observes again. Remaining owned objects produce an error with
an inventory.

The old `RouterNetwork.ConfigureNAT`, `LinuxNetwork.ApplyFirewall`, and
`LinuxNetwork.DeleteFirewall` methods are removed with their final consumers.
There is no permanent dual path.

## Typed Policy

A policy rule contains:

- direction: input, output, or forward;
- source and destination IPv4 CIDRs;
- source and destination port ranges;
- protocol: TCP, UDP, ICMP, or all;
- logical input and output attachment names;
- connection states: new, established, related, or invalid;
- verdict: accept, drop, or reject;
- an optional counter;
- optional rate-limited logging with a stable rule identifier.

Ports are valid only with TCP or UDP. CIDRs must be canonical IPv4 networks.
Logical interface names must resolve against the target resource's typed
attachments. Duplicate connection states and semantically duplicate rules are
rejected. Empty state lists mean any state; empty CIDRs, ports, and interface
names mean any value.

Input, output, and forward each have an explicit default verdict. The default
is drop when omitted. An allow-all policy must therefore be explicit.

NAT policy contains a logical source attachment, logical uplink attachment,
zero or more source IPv4 CIDRs, and a masquerade flag. NAT compilation is part
of the same table transaction as filter policy. A router requesting NAT without
a valid source and uplink attachment fails apply.

## Canonicalization And Identity

Each policy resource owns one table. Its table name is a deterministic,
nftables-safe value derived from the canonical topology identity and resource
address. The table includes an ownership comment containing the full owner
identity. Truncated table names are never treated as sufficient ownership
evidence.

Rules are normalized into declaration order after canonicalizing CIDRs, port
ranges, state sets, and logical attachment references. The canonical digest is
SHA-256 over a versioned serialization of semantic policy, resolved provider
interface bindings, hook configuration, and ownership identity. It excludes
nftables handles, counters, packet counts, timestamps, and rendering details.

Repeated apply replaces the complete owned table. It never appends a rule and
never mutates an unowned table. The same spec and attachment observation
produce the same digest.

## Atomic Application

The provider prepares the complete table, base chains, rules, NAT chain, and
ownership metadata in one nftables batch. Existing owned policy is replaced in
that transaction. Validation and expression compilation happen before the
batch is flushed.

After flush, the provider reads back the table, reconstructs the semantic
inventory, and calculates the digest. A missing table, incomplete inventory,
or digest mismatch fails apply. Runtime does not persist the new state until
this verification succeeds.

Router NAT failure is fatal. The current behavior that logs a warning and
continues with `nat_applied=false` is removed. Node/router rollback uses the
existing lifecycle cleanup path.

## Configuration And State

`sysbox_firewall` attaches to a router or node policy target, rather than a raw
network namespace. References use resource addresses and logical attachment
names. Its schema exposes the typed rule fields and explicit default policies.

Controlled egress is expressed as ordinary policy:

1. forward default drop;
2. accept established and related return traffic;
3. accept new traffic only to declared proxy endpoints or IPv4 CIDRs;
4. apply masquerade on the declared uplink when requested.

State records the policy owner, address family, deterministic table identity,
desired digest, observed digest, and normalized semantic policy. Provider
private state may store opaque execution data. Nftables handles and physical
interface names are never semantic state.

The state schema remains version 5 because this change does not alter the
top-level state serialization contract introduced by typed attachments.
Existing schema-v5 state containing the legacy firewall resource shape is not
silently adopted: refresh classifies its missing policy identity as drifted and
the next apply replaces it with the atomic ruleset.

## Refresh, Recovery, And Cleanup

Refresh calls `ObserveRuleset` and compares the observed digest:

- matching digest is present;
- missing table is drifted with not-found evidence;
- different digest is drifted with digest-mismatch evidence;
- unavailable observation is unknown and does not overwrite durable state;
- ownership conflict is an error and is never repaired by deleting the table.

Checkpoint recovery observes before applying. A completed external mutation
with a matching digest is adopted. A missing or mismatched table is replaced
atomically from the checkpointed semantic policy. Repeated recovery cannot
duplicate rules because replacement is table-scoped.

Destroy asks the owning provider to delete the deterministic table. The
provider verifies the full ownership comment, deletes the table, and scans for
owned table, chain, and rule residue. Destroy fails with a structured residue
inventory when any owned policy object remains. It never deletes an unowned or
ownership-conflicting table.

The old fixed `sysbox_fw` table and all Sysbox append-style iptables rules are
removed. Docker-managed bridge masquerading remains Docker daemon behavior for
`sysbox_network` resources; Sysbox does not claim or mutate Docker daemon-owned
tables. Router NAT and explicit Sysbox firewall policy use only the new owned
nftables path.

## Errors And Security

Policy diagnostics identify the policy resource and, where relevant, the rule
index and logical attachment. Unsupported family, invalid CIDR, invalid port,
unknown attachment, invalid protocol/state/verdict, and ownership conflict are
hard failures.

Default drop is enforced in kernel base-chain policy, not by relying on a final
rule. Established/related accepts are generated only when declared. Logging is
rate limited and cannot include secret configuration values. Digest and
inventory output contain policy structure but no secret material.

Sysbox never flushes a namespace-wide nftables ruleset and never deletes tables
based on name alone.

## Verification

Unit tests cover normalization, IPv6 rejection, deterministic table naming,
canonical digest, expression generation, connection-state encoding, reject
verdicts, NAT compilation, ownership conflict, and residue inventory.

Runtime tests with fake capabilities cover logical attachment resolution,
state persistence, digest-based refresh, observe-first recovery, fatal NAT
failure, replacement, and destroy error propagation.

Privileged tests create isolated network namespaces and verify:

- default-deny forwarding blocks undeclared traffic;
- an explicit IPv4 allow rule permits only the declared destination/port;
- established return traffic works;
- masquerade is present only on the declared uplink;
- repeated apply leaves one table and a stable digest;
- interrupted apply recovery converges to one complete table;
- destroy leaves no topology-owned table, chain, or rule residue.

Docker capability tests verify the same policy inside a real router container
when Docker and required kernel capabilities are available. Missing privileges
or provider dependencies are reported as skipped, never passed.

The final removal audit rejects production references to append-style
`iptables`, fixed `sysbox_fw`, and the removed driver methods. Full tests, vet,
focused race tests, topology plans, privileged capability tests, and
`git diff --check` must pass before the implementation is complete.
