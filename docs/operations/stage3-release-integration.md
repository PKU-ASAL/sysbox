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

Both the release archive builder and Dockerfile use this entry point. Archives
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
- Build and inspect the actual runtime image. Sharing the executable build
  script is not evidence that an OCI image has been built or deployed.
- Run upgrade/rebuild on an isolated deployment with archived authority and
  journals, then run the supported provider/transport matrix from the exact
  candidate artifacts. Existing stage 2 reports are layered evidence, not a
  same-build release run.
- Preserve historical unknown intent `p9-4221803d237b` and its waiting-for-stop
  barrier. Neither a new binary nor service restart proves the old effect stopped.
- Record candidate commit, archive hashes, OCI manifest identity and deployment
  evidence, then submit the final integration PR for review. Production rollout
  and main merge are separate actions.
