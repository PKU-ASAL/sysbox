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

## Remaining slices (not completed by the first slice)

1. Persist a pre-effect admission boundary before starting the runtime and use
   it to distinguish never-entered execution from an interrupted callback.
2. Capture provider-specific quiescence/operation identity for daemon requests,
   allowing independent observation without replaying unknown guest commands.
3. Reconcile interrupted Destroy and reset using those proofs, retain original
   action identity and require explicit continuation where effects remain.
4. Inspect retained historical journals and resources read-only. Records lacking
   the new proof cannot be silently converted; use independently verified host
   fencing and resource inventory before any authorized continuation.
5. Run real crash/partition gates, demonstrate no duplicate effect and exact
   resource cleanup, then update the overall completion tracker.
