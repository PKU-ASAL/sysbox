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
- [x] Review, run relevant checks, commit and create PR #50 with artifact evidence.

## Stage 2: GC and revision

- [x] Add explicit unknown provider-GC inspection and fencing evidence binding.
- [x] Reconcile/retire original commands and release reservations only after
  validated stop proof and fresh immutable-identity observation; preserve unknown
  outcomes and prevent replay across crashes.
- [x] Implement file retention batches with policy/reference revalidation and
  recoverable atomic publication, including late-reference races.
- [x] Define and implement remote registry consumer participation, offline
  consumer refusal and retirement admission across the supported backends.
- [x] Run scoped real libvirt/Firecracker disposition; review and deliver stage PR #51.
- [ ] Complete all API/Agent/provider-internal fault cases under P9.3; the
  historical e5ddeee 31-gate combined run does not close the full release matrix.

## Stage 3: ownership and P5/P6

- [x] Implement explicit ownership transfer plans, old-owner retirement proof,
  immutable resource binding and idempotent crash recovery.
- [x] Exercise supported host ownership-write/observe/state-commit crash boundaries
  for all three providers; API storage process interruption is covered separately.
- [x] Exercise state snapshot/restore/maintenance with old workers, independent
  process crashes and all supported storage backends.
- [x] Reconcile every P5/P6 requirement and deliver stacked PR #52. Full P5
  provider-internal/transport acceptance remains explicitly open in campaign.json.

Final P9 deployment-wide upgrade and release matrix stays separately tracked.

## P5 real host interruption follow-up

- [x] Extend `pkg/api/ownership_transfer_e2e_test.go` with a real libvirt
  provider child and parent-driven SIGKILL at before-effect, after-ownership,
  after-observe, before-report, after-state and after-report boundaries.
- [x] Recover through the coordinator and API: pre-return intents stay unknown
  with source barrier retained; durable returned outcomes complete state once.
- [x] Register the matrix as a strict real-provider gate and obtain review.
- [x] Extend real host interruption coverage to Docker and Firecracker and
  separately exercise API process interruption. A live API plus killed host
  cannot be counted as an API-process crash test.

- [x] Firecracker: six host SIGKILL boundaries after real KVM start/stop.
- [x] Docker: five host SIGKILL boundaries with effective ownership sidecar
  checks; callback reporting is not API persistence evidence.
- [x] API storage workflow: four independent-process SIGKILL boundaries across
  file, SQLite and PostgreSQL, including completed HTTP handler response loss.
- [ ] Provider-internal partial writes and every completion-handler intermediate
  boundary still require separate evidence; do not infer them from callback kills.

## 2026-10-22 adoption cancellation closure

- [x] Real whole-VM libvirt unowned/absent/source and Firecracker prepared
  source/source-absent cancellation; preserve unknown history and source retirement.
- [x] Fixed scenario matrix, correct cancellation gate selectors, and precise
  retained-object GC reference assertions; operator boundary documented.
- [x] Remove obsolete P5.3/P8.2 functional blockers and distinguish historical
  combined evidence from current-release verification. No main merge/deployment.
