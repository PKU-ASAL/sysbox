# Compatibility, GC and ownership closure plan

Approved sequence: Docker compatibility delivery, GC/revision closure, then
ownership transfer and P5/P6 crash acceptance. Each stage produces local commits
and a reviewable PR. Do not merge or deploy production services implicitly.
Historical labs remain read-only; destructive fencing is limited to enrolled
isolated execution VMs.

## Stage 1: Docker compatibility

- [x] Test ResolveImage through the Docker HTTP client: config/manifest digest,
  unknown digest rejection, stable identity, publication callback preservation.
- [x] Adapt PR #49 without dropping artifact publication fencing.
- [x] Include Docker 29 CLI in the runtime image; verify real old/new daemon
  compatibility and built image binaries.
- [ ] Review, run relevant checks, commit and create a PR with artifact evidence.

## Stage 2: GC and revision

- [ ] Add explicit unknown provider-GC inspection and fencing evidence binding.
- [ ] Reconcile/retire original commands and release reservations only after
  validated stop proof and fresh immutable-identity observation; preserve unknown
  outcomes and prevent replay across crashes.
- [ ] Implement file retention batches with policy/reference revalidation and
  recoverable atomic publication, including late-reference races.
- [ ] Define and implement remote registry consumer participation, offline
  consumer refusal and retirement admission across the supported backends.
- [ ] Run real libvirt/Firecracker disposition and full API/Agent fault cases;
  review, commit and open a stage PR without shrinking original requirements.

## Stage 3: ownership and P5/P6

- [ ] Implement explicit ownership transfer plans, old-owner retirement proof,
  immutable resource binding and idempotent crash recovery.
- [ ] Exercise ownership-write/observe/state-commit crash boundaries.
- [ ] Exercise state snapshot/restore/maintenance with old workers, independent
  process crashes and all supported storage backends.
- [ ] Review and reconcile every P5/P6 requirement; commit and create stage PR.

Final P9 deployment-wide upgrade and release matrix stays separately tracked.
