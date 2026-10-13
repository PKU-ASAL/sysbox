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
