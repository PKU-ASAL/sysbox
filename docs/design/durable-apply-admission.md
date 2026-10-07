# Durable keyed Apply admission

## Contract

`POST /v1/topologies/{topology}/apply` accepts an optional `Idempotency-Key`
(1–128 ASCII letters, digits, colon, underscore or hyphen). With this header,
`revision` must identify an immutable global content-addressed revision. Existing
unkeyed requests retain their previous upsert and terminal re-execution behavior.

The server derives a run identity from topology, operation and key. A durable
fingerprint covers topology, revision, inputs, unsafe-state flag, explicit agent
and absolute deadline. Replays return the original run, including terminal runs;
changing any fingerprint parameter returns 409. A new execution needs a new key.
Store failures are errors, never evidence of absent acceptance.

SQLite and PostgreSQL atomically persist request identity, run and one agent
command. PostgreSQL admission uses a per-topology transaction advisory lock;
SQLite's write transaction serializes admission. Conflicting active operations
are rejected. A single API instance additionally serializes workspace writes,
keyed lifecycle admission, deletion and cleanup inspection by topology. Multiple
API instances sharing workspace files are not a supported general execution
configuration: the two-instance tests establish atomic same-key deduplication,
not cross-instance filesystem orchestration.

## Ephemeral sensitive inputs

Only the fingerprint is stored. Plain inputs are absent from run rows, request
rows and command rows. The run claim response receives inputs from a separate
in-memory hydration map. After API restart, an active keyed Apply cannot be
claimed until the caller resubmits the same request/key to that API. The claim
endpoint returns 503 without acquiring a run lease. The agent retries 503 with cancellation support, retaining the original command,
run and per-execution claim identity.

`GET /v1/runs/{id}` returns `inputs_required: true` when this API requires that
replay. The client must replay at the agent-connected API (or use deployment
session affinity); this design does not pretend that another API's memory is
shared. After hydration, the original command can continue. Terminal replay
never restores inputs or schedules new work. GET run returns 503 on backend
read failure, and only an authoritative not-found result returns 404.

## Authoritative cleanup

A keyed Destroy of a workspace whose HCL is absent first reads persistent state
and confirms no active topology operation. If state contains no resources, the
server records an immutable successful run receipt with no agent command. State
errors never produce successful receipts. Missing HCL with live state resources
continues through persisted placement and destruction rather than metadata force.

`GET /v1/topologies/{topology}/cleanup-inspection` returns resource identities,
active operation identities, `state_exists` and `metadata_exists`. It works for
never-created and already-deleted workspaces. Backend, inventory and metadata
read errors return 503. Private state and resource attributes are excluded.
Clients must reject nonempty resources or active operations before deletion.
Deletion shares the local topology lock and rejects active operations; it does
not imply runtime destruction. The normal no-force metadata deletion path is
idempotent for an already absent workspace.

## Validation and limits

Regression tests cover terminal replay, fingerprint conflict, accepted-response
loss modeled by restart before consuming the receipt, input rehydration, secret
nonpersistence, two PostgreSQL API instances, active-operation conflict, run
backend failure, cancellable agent retry, and never-created Destroy → inspection
→ deletion → independent archive receipt. API and agent suites pass under race
with a disposable PostgreSQL fixture. This change does not alter provider drivers,
resource semantics, or claim that filesystem/backend rollback undoes effects.

## Durable claim response recovery

Current agents assign a random UUID to each execution instance before its first
claim request. SQLite and PostgreSQL compare this identity, encoded in the lease
owner, inside the claim transaction. A repeated claim for the same agent and
unexpired running lease returns the existing receipt; it does not increment the
attempt, change the deadline or renew the lease. A new process or execution gets
a different UUID and cannot take over this lease. Terminal/expired leases reject
replay. PostgreSQL uses the locked run row; SQLite reads and writes one transaction
snapshot, mapping write contention to retryable backend failure. No schema change
or durable sensitive input is required.

The `claim_replay` capability is advertised only by durable API stores. Agent
transport errors and truncated responses are unknown delivery and retry the same
identity, alongside 502/503/504. Authentication, conflict and other explicit
rejections stop retrying. Retries are bounded by five minutes, the caller context
and the run's fixed deadline. On API restart, same-key Apply rehydration precedes
receipt recovery. Same-key concurrent store tests, restart/rehydration tests and
actual HTTP disconnect/truncation tests cover this boundary under the race
detector with SQLite and disposable PostgreSQL. An agent-process crash after
execution starts remains a lease-expiry/recovery concern; these semantics are
receipt recovery, not a claim of exactly-once infrastructure effects.
