# Stage 3 release integration

Stage 2 is closed under the boundaries in [its closure ledger](stage2-closure.md).
Stage 3 remains open. This document separates completed delivery fixes from
outstanding integration evidence.

## Unified executable build

`scripts/release/build-binaries.sh OUTPUT [LDFLAGS]` builds Linux executables for
`GOARCH` (amd64 or arm64). It compiles both guest helpers first and supplies them
through a Go build overlay when compiling `sysbox`. It does not overwrite the
checkout's embedded helper files. API, agent and host supervisor are subcommands
of that same `sysbox` executable.

The release archive builder and default source Docker build use this entry point.
Release OCI builds instead consume these verified archives directly. Archives
include `sysbox`, `sysbox-init`, and `sysbox-netns`; their hashes participate in
the release fingerprint. `make release-test` checks that the shipped helper's
exact bytes occur in the CLI and that repeated builds produce identical archives.
This fixes the previous build order, which could embed a stale helper or the
committed placeholder while shipping a freshly compiled standalone helper.

For an untagged candidate (not a published version):

```sh
GOCACHE=/tmp/sysbox-gocache scripts/release/build.sh \
  --tag v0.1.0 --allow-untagged --output /tmp/sysbox-stage3-candidate
GOCACHE=/tmp/sysbox-gocache scripts/release/verify.sh /tmp/sysbox-stage3-candidate
```

The candidate tag is a test label only. Do not publish it as a real release.

## Docker compatibility integration

Remote commits `59e6428` and `05d4e33` were adapted on the stage 3 branch.
The refactor already had equivalent digest matching and a Docker 29 CLI.
The integration imports the remote helper functions and tests while preserving
artifact publication admission and completion callbacks. Container creation
continues to use the daemon-local image ID; portable artifact identity uses
the verified digest lock.

## Remaining closure gates

- Main `b1cc43d` semantics are reconciled below. The feature branch retains the
  replacement architecture; production main is not updated by this integration.
- Publish and verify the multi-platform runtime manifest. Local amd64 runtime
  assembly and executable identity passed; arm64 runtime and registry delivery
  have not yet been exercised.
- Run upgrade/rebuild on an isolated deployment with archived authority and
  journals, then run the supported provider/transport matrix from the exact
  candidate artifacts. Existing stage 2 reports are layered evidence, not a
  same-build release run.
- Preserve historical unknown intent `p9-4221803d237b` and its waiting-for-stop
  barrier. Neither a new binary nor service restart proves the old effect stopped.
- Record candidate commit, archive hashes, OCI manifest identity and deployment
  evidence, then submit the final integration PR for review. Production rollout
  and main merge are separate actions.

## Runtime candidate evidence

Local image candidate `sysbox:stage3-36dfad7-verified` was assembled from the
verified release archive, rather than rebuilt in a second Go toolchain. Evidence
was recorded in `/tmp/sysbox-stage3-36dfad7/runtime-evidence.json`:

- version commit `36dfad78bb3ec6cef53e60c2ba4dc5e75d422d43` and build time matched
  archive metadata;
- runtime Docker CLI reported `Docker version 29.9.1`;
- `sysbox`, `sysbox-init`, and `sysbox-netns` hashes matched the amd64 archive;
- the scope was local amd64 image assembly. No registry push or production
  service restart was performed.

The OCI publisher now verifies the archive directory and supplies it as the
`release` named build context. The Dockerfile validates archive checksums,
metadata, and target executable hashes before assembling the runtime layer.
A future multi-platform registry run must prove the manifest digest and both
child-platform labels with `oci.sh verify`.

## Main behavior audit and receipt recovery

A source review against main `b1cc43d` found three concrete omissions in the
refactor, now implemented:

- Run claims carry a stable per-execution UUID across retries. Live identical
  retries recover the existing lease without renewal or attempt increment.
  Incarnation checks, missing-input refusal and expired-lease rejection remain.
- Completion projections persist atomically with the terminal Run. Both replay
  and API startup restore the original receipt; a modified retry cannot replace
  it. Publication preserves newer observations. Ordinary save/restart/dispatch
  cannot inject a completion projection.
- Claim request bodies are bounded before signature verification.

Validation includes the API, agentexec, persistence and controlplane package
suites; targeted race tests; and real dedicated PostgreSQL tests using isolated
schemas, alongside file and SQLite backends. The response-loss and projection
receipt tests failed before implementation. Evidence logs:

- `/tmp/sysbox-stage3-main-regressions-final.log`
- `/tmp/sysbox-stage3-replay-race.log`
- `/tmp/sysbox-stage3-postgres-replay.log`

The two remaining semantic differences were resolved in the following audit.
Keyed admission and canonical action recovery retain their replacement
implementations instead of restoring the old checkpoint-based execution path.

The earlier runtime image evidence applies to executable commit `36dfad7`, not
the newer receipt changes. Final release acceptance must rebuild the candidate.
The old upgrade archive script still hardcodes `final-1018` and is not a completed
rebuild rehearsal for the current lab; that missing deployment was detected before
any service mutation.


## Main integration decisions

The reviewed upstream range is `8628abb..b1cc43d` (PRs #43, #44, #45).
The feature branch records upstream ancestry while retaining its audited tree:
upstream fixes are ported through the new repositories and host authority, rather
than reintroducing the replaced persistence/execution implementations.

| Upstream behavior | Integrated implementation and boundary |
| --- | --- |
| Durable keyed admission and input hydration | Existing `keyed_apply.go`, atomic revision dispatch and hydration tests retained. |
| Lost claim response and completion projection | `1479638` adds stable claim receipts, startup projection recovery, bounded claim bodies and proof mutation guards. |
| Lease valid when writing | Worker renewal/completion carry an explicit live-lease requirement. SQLite INSERT predicates and PostgreSQL UPDATE predicates recheck the original lease and deadline using database time. File storage rechecks under its write lock. Controller expiry and signed host recovery remain separate. |
| Unknown command never replayed | Aged running/acknowledged commands stay non-leasable. No timeout fabricates a stop or terminal operation. Signed stop tombstones forbid a stopped power action from acquiring another epoch, including after a later action replaced the latest scope grant. |
| Lost terminal node delivery | Canonical operation identity/status gates power grant issuance. Command reconciliation reads the canonical terminal operation, preserves lease CAS, and reloads the persisted winner. |
| Interrupted effects | Canonical action intents and signed stop evidence replace old checkpoint replay. Existing supervisor approval and effectgate tests retained. |
| Protocol advertisement | `sysbox-runtime-v1` retained intentionally; `claim_replay` restored. Rebuild/upgrade requires coordinated API/Agent versions, not silent legacy fallback. |

A generic power stop receipt proves cessation, not a durable operation result.
If worker completion was lost, the operation stays unconfirmed and cannot mint
a new epoch. Its evidence must remain available for explicit disposition; do not
infer pause/resume success from the stop receipt alone.

Verification: delayed-write tests first reproduced expired renewal/completion
on file and SQLite, then passed on all three backends, including dedicated
PostgreSQL. Signed real Docker pause/resume, lost-acknowledgement and cold-host
recovery acceptance passed. Independent review found no blocking issues.
The old power-grant fixture was corrected to persist the canonical operation
through atomic dispatch, rather than creating only a command.

Evidence:

- `/tmp/sysbox-stage3-postgres-write-power.log`
- `/tmp/sysbox-stage3-power-real.log`
- `/tmp/sysbox-stage3-write-power-race.log`
- `/tmp/sysbox-stage3-api-final.log`

These are main-integration checks, not a same-artifact deployment acceptance or
an upgrade/rebuild rehearsal. The final candidate still needs rebuilding.

## Candidate b80e231 deployment acceptance

The release archives for `b80e231` were built and verified for amd64 and arm64
in `/tmp/sysbox-stage3-b80e231`. Acceptance/deployment tooling now accepts release
metadata and validates executable hashes. The mixed lifecycle runner uses the
supplied CLI/helper without recompiling them; its independent probe is still
built from the recorded harness source. Deployment installs and records all three
executables, including `sysbox-netns`.

Completed evidence:

- `/tmp/sysbox-stage3-mixed-b80e231/run.json`: release CLI/helper passed real
  Docker/Firecracker/libvirt Apply, Reset, Destroy, six-direction communication,
  cross-host NAT, and exact absence for both node generations.
- `/tmp/sysbox-gc-fencing-20261021/upgrade-286925cc7d41/evidence.json`: old API,
  Agent and supervisor were already inactive; selected database dump and control /
  execution archives were hashed. The unresolved journal remains unchanged in
  the original disk and archive. Old VMs were stopped and their disks retained.
- `/tmp/sysbox-stage3-rebuild-20261026/stage3/services.json`: a separate clean VM
  pair and dedicated `sysboxstage3rebuild` database received the release binaries.
- `stage3/lifecycle.json`, `stage3/provider-absence.json`, and
  `stage3/restart-check.json` under that new lab: API-to-Agent Apply/Reset/Destroy,
  both Docker incarnation absences, metadata DELETE, and persisted terminal Runs
  after API restart passed. Host executable hash files match the release metadata.

This is explicit fresh-deployment rebuild, not old-state migration or resolution
of historical unknowns. Old schema rejection has existing repository tests; this
live rehearsal did not import an old schema into the new deployment. Likewise,
the provider matrix is CLI-driven while the fresh API deployment lifecycle uses
Docker; do not describe this as every provider over every transport in one run.

The archive helper now supports `--deployment` and `--archive-only`. Archive-only
requires both hosts' service pairs to already be inactive and never starts them.
Malformed or foreign journals are recorded as unknown and archived raw. Normal
service-upgrade mode still refuses unresolved journals. This also removes the
broken `find -exec`/jq placeholder substitution in the old journal scanner.

Remaining delivery boundary: multi-platform OCI manifest build/push/verification
has not passed. The shared host's builders only support amd64/386 and an arm64
container failed with exec-format error. Arm64 emulation was installed only in
the new disposable execution VM; it has not yet been used for a successful OCI
build. VM Docker Hub access timed out, so its Docker lifecycle used an image
transferred from the local cache. No production rollout or stable tag publication.

## Multi-platform OCI archive evidence

The shared Docker daemon lacked arm64 execution support initially. After enabling
arm64 emulation and using the configured internal mirror for base images, the
isolated `sysbox-stage3-multi` builder produced
`/tmp/sysbox-stage3-runtime-index.tar`. The archive contains linux/amd64 and
linux/arm64 child manifests. `scripts/release/verify-oci-archive.py` verified
both children carry version `v0.1.0`, commit `b80e23136159586806f6b4ab82fa9c5845c7b931`,
MulanPSL-2.0, and fingerprint
d816f6742b7faaf3a94a653757720fad91631c0237608ade5cbe2331c03fbe28. Output is
recorded at `/tmp/sysbox-stage3-oci-archive-evidence.json`.

Dockerfile base references are now build arguments, allowing CI or an approved
registry mirror to supply reachable multi-platform bases without changing the
release executable identity. The registry publisher still requires its own
preflight and immutable tag reconciliation. No registry push or stable tag
publication was performed.
