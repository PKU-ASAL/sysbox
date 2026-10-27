# Durable external GC sources

Goal: make external references participate in the same durable aggregate and
reservation lock as local references, rather than treating a callback as authority.

The approved direction is persistent registration, version binding, and
publication/deletion exclusion. A periodic manifest alone cannot prevent writes
after observation. Source writers therefore begin a publication before changing
external state; the updating marker blocks GC until a complete manifest commits.
A lost publisher remains updating. A complete manifest expires for authorization
but is never erased or interpreted as an empty reference set.

Source registration upgrades only that aggregate to format 7. Existing format 6
aggregates remain readable and writable; old binaries reject format 7 instead of
ignoring external protection. Enrollment is explicit. Snapshots retain source
references; restore does not rewind current source registrations.

Tasks:
- [x] Add persistence regressions for incomplete publication, freshness, stale
  plans, exact resource protection, cross-connection reservation exclusion, and
  cold reopening.
- [x] Implement BeginGCSource and CompleteGCSource with CAS, full four-domain
  manifests, immutable owner binding, and atomic aggregate validation.
- [x] Merge durable source references in gcReferences so reserve, batch, grant,
  renewal, and execution authorization consume the same digest and references.
- [x] Expose authenticated topology-scoped source publication endpoints; strict
  JSON decoding and existing API error mapping.
- [x] Add API route tests, focused race tests, full Go checks, docs, and review.

Boundary: this installs the publication protocol. Existing arbitrary external
writers do not become participants automatically. Production revision/artifact/
cache/placement writers must adopt begin-before-use semantics before claiming
cross-host deployment acceptance. This implementation must not close P8.2 until
those writer integrations and real interruption tests are present.

Review caught post-reservation expiry deadlock; the immutable reservation now
pins freshness until receipt while continuing to reject updating sources.
Independent review found no remaining blockers. Real Docker source publication
and single-delete/cold-receipt recovery passed.

## Production integration continuation

- [x] Install stable per-service source configuration and cumulative immutable
  reference publication in workspace/materialization and placement writes.
- [x] Carry publication context through host/remote execution into runtime and
  provider artifact resolution, including OCI and mutable URL invalidation.
- [x] Reject missing configuration and unsupported local CLI for enrolled state.
- [x] Verify actual download intent, concurrent GC refusal, independent-process
  kill, cold fail-closed reading, and independent workspaces on three stores.
- [ ] Deployment-wide enrollment and global registry/shared-cache deletion are
  separate remaining requirements; metadata retirement is not implemented.
