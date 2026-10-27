# Unknown Run recovery workstream

The remaining objective is safe convergence of intent-only Runs and interrupted
reset/Destroy, including retained incidents. A stopped controller or an absent
checkpoint does not prove a daemon request stopped. Resource presence is not
proof that provisioners, guest operations or deletion completed.

## First slice: synchronous failure before action admission

After the trusted executor returns failed, reacquire topology exclusion and read
the entire action journal. A non-continuation Run with no action of its own and
no unrelated unresolved action can be signed failed once child admission is
closed and all children have acknowledged stop evidence. This does not produce
a cleanup receipt or permit reset continuation. Historical successful actions
are retained. Journal read/lock failure or unknown child admission keeps the Run
uncertain. Only the live returned executor invokes this path; cold recovery
cannot derive this proof from an empty journal.

Validation: signed terminal evidence through the real host coordinator and
executor on invalid HCL; rejection with any own action, unrelated unresolved
action, continuation or unknown child; preservation of existing safe-reset
phase handling; focused race and full regression.

## Second slice: crash after admission, before runtime entry

The production host reads the canonical running Run before admission and stores
its immutable request and stripped Run incarnation in the same gate transaction
as the intent. This versioned admission evidence excludes inputs and cleanup
claims. Before the callback can launch, the gate rechecks online authority and
persists an irreversible Started marker. Renewal preserves both fields.

Recovery scans only records carrying explicit admission evidence without the
Started marker or a resource checkpoint. It takes the original process-independent
executor lock, rereads the current envelope and validates the recorded Run/request
against the signed grant before storing failed. Existing host receipt sealing,
API incarnation validation and authoritative child checks still govern release.
It does not produce a cleanup receipt or replay a callback. Historical records
without admission evidence remain uncertain; missing fields are never upgraded.

Validation includes a subprocess exiting immediately after admission without
unlocking, cold receipt recovery after boot identity change, alive-executor
exclusion, altered identity and entered/old-record rejection, and canonical API
failure/dispatch retirement/authority release. Started records remain unknown
unless the separate checkpoint/provider proof path establishes quiescence.

## Third slice: deletion returned, publication failed

After the synchronous Destroy executor returns failed, the host reacquires
topology exclusion and reads the full journal. Every action of this incarnation
must be a delete with EffectComplete, exact candidate identity and a succeeded
or uncertain status. No unrelated action may remain unresolved. Once child stop
evidence is confirmed, the host signs recoverable failed without a cleanup proof.
The normal owning-agent recovery path may then observe absence and commit the
existing deletion outcome without another provider call.

Authenticated host completion closes the canonical Run lease (LeaseUntil zero),
retaining attempt/owner and the original signed evidence. A future worker lease
deadline must not block observation of an already-proven stopped execution.
Ordinary timeout and unverified worker failure do not use this transition.

The real Docker gate injects a publication failure after provider deletion
returned, reconstructs API/host, observes and commits recovery, and then finishes
Destroy/metadata cleanup. It counts provider deletion calls and requires exactly
one before and after recovery. This does not cover a missing provider response,
process death before EffectComplete, or historic stopped-but-not-removed nodes.

## Fourth slice: lost Docker removal response and cancelled evidence write

Docker removal errors now trigger one bounded read-only inspection of the full
container ID resolved before deletion. Only definitive NotFound establishes the
postcondition; existing/transitional containers and daemon errors remain unknown.
The DELETE request is never retried. A separate read context preserves the chance
to obtain evidence when the original caller was cancelled.

After a successful deletion callback, runtime persists EffectComplete without
the cancelled execution context, bounded to five seconds. This is a journal-only
write with normal CAS/fencing checks. Further effects and state publication still
use the original context and stop on cancellation. Cold recovery can then observe
and publish the original deletion outcome without repeating it.

Docker observation rejects incomplete state/identity and restarting/removing/dead
states instead of classifying them as stable running/exited or panicking. A
positive container inspection is not proof that the daemon has quiesced.

The real-provider test discards the actual successful DELETE response and cancels
the caller, checks exactly one DELETE, and independently confirms exact-ID
absence. Runtime cancellation tests verify evidence survives but state is retained
until observation-only recovery. This still does not resolve the historical
stopped-but-not-removed object or requests whose absence cannot be verified.

## Fifth slice: cold host after durable deletion return

The host scans entered Destroy intents with new-protocol admission evidence and
no resource checkpoint. After taking the original executor lock and rereading
the signed request, a trusted callback takes topology exclusion and reads the
canonical Run, state and full journal. Incarnation/attempt/owner must match the
admission; all deletion actions must have durable EffectComplete. Local task
inventory must be readable and every matching child must have a verified,
acknowledged stop. The authority API still independently checks its child grant
inventory before releasing the parent.

The host writes only recoverable failed, without Cleanup, preserving the original
Run identity. It does not repeat provider effects. The existing public owning-
agent recovery subsequently observes absence and commits the original action.
Live executor locks, changed Run identity, unknown action phases, missing task
inventory and unfinished child records all reject this recovery path.

Real Docker acceptance pauses publication after a deletion-return record, stops
the host, checks that no Run completion was published, reconstructs API/host and
requires cold recovery before normal observation/Destroy. The provider deletion
count remains one. This covers cooperative host-service interruption at a durable
boundary, not a SIGKILL in an arbitrary daemon call or an old journal lacking the
admission seed. Historical stopped-but-not-removed records remain separate.

## Sixth slice: cold reset at a safe durable boundary

Reset and Destroy now share the interrupted-Run recovery entry point and
execution/child exclusion. Reset reuses the existing phase validator: accepted,
prepared, destroy_returned, destroyed and verified completion may supply failure
evidence. Preparing, destroying and incomplete applying remain uncertain. The
Run must have a valid admission seed. Cold recovery of an interrupted reset
continuation now resolves its ancestry and verifies every predecessor's stopped
execution proof before consulting the root-owned action journal. Missing stop
proof, changed identity, cycles and excessive depth remain rejected. The failure
receipt belongs to the current execution; action IDs and ParentID audit history
are never rewritten. Destroy continuations remain unsupported by this path.

Recovery signs failed with no cleanup and requires an explicit public resume.
The continuation retains the original action ID and reset handle rather than
preparing a replacement action. Privileged host bootstrap binds its own Agent
identity/public key to the execution bridge so original host-stop receipts can
be verified locally; conflicting or changed bindings are rejected.

Real testing exposed an observation bug in Docker: a prepared reset whose old
container still occupied the intended name was checked as a replacement. The
observer now recognizes only the exact original ID with its recorded old labels
as prepared. Foreign ownership/generation and old-ID reappearance after a new
replacement ID is recorded are still rejected.

The Docker gate interrupts the host after prepared evidence is persisted,
reconstructs API/host, resumes through the public endpoint and requires exactly
one preparation, one old-container deletion, one replacement and one original
reset action. It then completes Destroy and metadata cleanup. This is not a
claim of recovery from an unknown in-flight preparation/destruction call or of
historical records without the admission proof.

Repeated-interruption acceptance stops after prepared, resumes, stops again after
destroyed, then resumes a second time. It requires one preparation, one deletion,
one replacement and one original action throughout the chain, followed by normal
Destroy and cleanup. This adds no authorization to replay unknown phases.

## Remaining slices

1. Extend real process-fault coverage of the new admission boundary to the
   distributed lab; the local subprocess and API contract are already covered.
2. Capture provider-specific quiescence/operation identity for daemon requests,
   allowing independent observation without replaying unknown guest commands.
3. Reconcile interrupted Destroy and reset using those proofs, retain original
   action identity and require explicit continuation where effects remain.
4. Inspect retained historical journals and resources read-only. Records lacking
   the new proof cannot be silently converted; use independently verified host
   fencing and resource inventory before any authorized continuation.
5. Run real crash/partition gates, demonstrate no duplicate effect and exact
   resource cleanup, then update the overall completion tracker.
