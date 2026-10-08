# Agent crash fencing

## Goal and ownership

An agent process crash must not permit stale completion, resurrect an expired
lease, or silently replay a command whose external effect is unknown. API and
agent remain single-instance services with a persistent agent runs volume.

The existing persisted `lease_owner`, attempt and lease deadline form the run's
execution incarnation. Each agent process generates a UUID; each claim derives a
fresh claim UUID from that process incarnation and a fresh execution nonce.
Transport retries reuse that UUID. No second agent-session table is introduced.
A new process cannot renew or complete another claim. Starting a replacement
process does not preempt a still-live owner: takeover waits for lease expiry.

## Implemented steps

- [x] Reproduce expired renewal, stale completion, lost ACK and pre-effect write failures.
- [x] Use atomic compare-and-swap for run completion, expiry and convergence timeout, plus command delivery, events and recovery in SQLite and PostgreSQL.
- [x] Persist the canonical completion projection in the same run CAS (additive SQLite column, existing PostgreSQL JSON); repair caches after response loss or API restart without trusting replay bodies.
- [x] Cancel execution when renewal fails or the previous lease deadline expires; renewal cannot mutate the executor's shared run pointer.
- [x] Persist a secret-free local command inbox record with file and directory fsync before ACK. Run commands ACK only after durable claim succeeds.
- [x] Reject duplicate command/run ownership in one process and block persisted inbox replay across process restarts.
- [x] Converge acknowledged/running run commands from the authoritative run outcome, including after API restart; scan commands with a bounded, rotating 128-record page.
- [x] Fail closed before external effects when checkpoint persistence fails. File checkpoints use atomic rename and file/directory fsync.
- [x] Require complete run-scoped labels and exactly one candidate for label recovery; propagate inspection failures as unknown.
- [x] Block resume for incomplete effects, absent parent records and unresolved recovery actions.

## Recovery bounds and operator actions

The configured run lease is the upper bound for a lost agent to become
`failed`/`recoverable` (default 30 minutes), plus the supervisor scan interval.
Command projection convergence adds at most one traversal of command pages;
each scan handles 128 records and retains command terminal state so late events cannot recreate removed rows. Pending delivery and keyed lookup use separate queries, so retained history cannot hide pending work. Successfully
renewed deadlines fence stale expiry snapshots.

Non-run commands have a 30-minute maximum execution window in the agent. An
acknowledged/running command whose window expired becomes failed with
`outcome unknown; manual recovery required`. Guest execution/file records become
terminal before the command record so a late receipt cannot authorize replay.
Unacknowledged delivery leases can be redelivered after their delivery lease
expires. A durable inbox record prevents a second effect if the ACK was lost.

Retain the agent runs volume, including `_agent_commands/runs/`. Inbox records
contain command ID, type and process incarnation only, never request bodies or
inputs. An existing inbox record is not permission to replay. Inspect the run,
checkpoint and actual provider resource first. A complete state receipt can be
adopted/replayed by the existing checkpoint recovery path. Missing receipts,
ambiguous labels or incomplete guest effects remain blocked. Do not delete inbox
records to force reexecution of an uncertain command.

This change does not implement transparent resume of arbitrary guest programs,
external provider-side fencing, or multi-agent takeover. Context cancellation
cannot undo an effect that a provider already accepted. Such effects require
checkpoint/provider reconciliation before a new business operation proceeds.

## Validation

Unit regressions cover lease expiry, stale incarnation completion, same-claim
replay, ACK ordering, local inbox persistence, missing-parent fallback, exact
labels and pre-effect checkpoint failure. SQLite and PostgreSQL tests cover CAS
contention between completion and expiry, stale expiry after renewal, and API
restart recovery. The affected packages are checked with the Go race detector.

Additional regression evidence covers an injected provider success immediately
before checkpoint receipt persistence: the durable pre-effect labels remain in
the journal, the recovery report exposes `blocked_unconfirmed_effect`, and the
next guest effect cannot execute. Only complete state receipts are adopted;
label identity alone does not synthesize missing state or authorize replay.
The agent does not increase a shorter API lease to its 30-minute default during
renewal, so a 10-second API lease remains usable for isolated crash tests.

Inbox/checkpoint creation fsyncs every ancestor directory as well as the file
and final directory entry. Renewal UPDATE and completion INSERT/UPDATE check the
database clock at the write statement. This is a write-time authority check,
not a promise that a transaction cannot finish after an arbitrary database
trigger or storage stall. Failed or uncertain writes never authorize another
execution attempt.

The recorded validation here is unit/integration fault injection, not a claim
of a real agent-process kill or Docker provider crash test. Those deployment
checks are performed separately against the pinned API and agent image.
