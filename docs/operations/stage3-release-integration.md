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

- Main `b1cc43d` has diverged from the refactor. A trial merge produced
  conflicting paths and was aborted; no main merge has been completed.
  Audit the durable admission, claim replay and execution-fencing requirements
  against the replacement architecture before resolving that ancestry.
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

The main ancestry gate is still open for two explicit semantic decisions:
application-clock lease checks after acquiring the storage lock versus main's
database write-time clock checks; and generic legacy node-operation unknown
commands versus current signed host-operation recovery. Do not restore a generic
timeout/replay path that bypasses host stop evidence. Keyed admission and canonical
action recovery already have replacement implementations and should be retained.

The earlier runtime image evidence applies to executable commit `36dfad7`, not
the newer receipt changes. Final release acceptance must rebuild the candidate.
The old upgrade archive script still hardcodes `final-1018` and is not a completed
rebuild rehearsal for the current lab; that missing deployment was detected before
any service mutation.
