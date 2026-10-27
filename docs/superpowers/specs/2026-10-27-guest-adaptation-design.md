# Guest capability adaptation

The user approved two sequential PRs against dev: Firecracker policy first,
Windows durable tasks second. Both must cover normal completion, lost responses,
host/guest restart during execution, and delayed old-generation requests.

## Firecracker

Reuse signed guest_task admission, immutable request digests, detached workers,
cgroup subtree supervision, host Run child journals and signed stop receipts.
A reserved, narrowly validated built-in policy task executes the installed helper
with privilege; ordinary commands remain uid 65534. Its JSON input carries both
node identity and policy identity. Guest admission binds node identity to the
grant scope; no caller-selected executable, environment or workdir is accepted
for this task. The helper must verify the signed request again before effects.

A guest-local policy journal persists prepared/active/deleting/deleted phases,
policy generations and digests independently of transient network namespace
inodes and boots. One serialized nft transaction replaces the table. Observation
never performs mutations. Lost responses recover through the existing task
record and actual nft state; a reboot is not fabricated as successful execution.

## Windows

Preserve the same public task/grant/receipt semantics. A Windows guest helper
must use Job Objects, suspended launch followed by job assignment, durable
request IDs, boot identity, bounded output and expiry/stop handling. QGA carries
protocol requests rather than replaying arbitrary guest-exec after ambiguity.
File publication and ETW bootstrap must obey the same lifecycle boundaries.
Artifacts must be digest-pinned and portable. No unsupported transport may
advertise a capability until its implementation and acceptance pass.

## Acceptance and reporting

Each PR records four fixed acceptance cases and states whether evidence is
unit/process integration or real VM. Historical unknown intents are preserved.
Only isolated acceptance VMs may be restarted or powered off; real historical
hosts receive read-only plans. Feature enablement follows complete admission,
execution, observation and stop/recovery wiring, not just compilation.
