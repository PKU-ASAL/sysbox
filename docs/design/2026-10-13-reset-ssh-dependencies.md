# Reset SSH dependency repair

The reset journal must own guest-local dependencies as well as the node. Merely
changing the SSH handle would invent evidence; ignoring incarnation checks could
delete a replacement's keys. Rebuilding SSH outside the journal would leave an
unrecoverable gap between node replacement and permission creation.

Pin each existing SSH dependent, desired configuration digest and stable child
action ID in the reset intent before any node mutation. After node setup reaches
the durable verifying phase, use the replacement node candidate to discover or
create each SSH configuration. Persist child admission before mutation. On cold
recovery a previously admitted mutation is observed by its original identity;
absence is uncertainty, never permission to repeat it. A matching committed
result may be recorded without replay. Foreign identities and changed desired
configuration fail closed.

The repository validates immutable dependency snapshots and monotonic child
evidence. Final reset commit atomically replaces the node and SSH resources.
Until all children have verified results, the reset cannot be committed as
succeeded. Existing old-incarnation records remain intact on failure.

Implementation and validation sequence:

- [x] Restore the real Docker SSH + targeted reset + Destroy failing case.
- [x] Extend the reset action journal with pinned dependencies and child results;
  validate immutable snapshots and atomic multi-resource publication.
- [x] Reconcile dependencies in verifying, observe uncertain child admissions,
  and test cold recovery, foreign results and changed configuration.
- [x] Run real Docker combined workflow, focused race, full tests and review;
  update acceptance evidence and commit.

Scope: existing managed SSH dependents of reset nodes. Preserve the public SPI
and reject old formats; no compatibility path or historical evidence rewrite.

Local CLI authority resolves an admitted reset candidate only for the exact
started child ID and pinned SSH configuration. Public API admission cannot
supply this candidate. Read-only recovery can observe and publish matching
results but cannot start a new mutation. Unknown admitted mutations remain
uncertain; losing an intent is never interpreted as permission to replay.
