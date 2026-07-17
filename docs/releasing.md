# Releasing Sysbox

Sysbox uses Forgejo Actions and explicit stable SemVer tags. CI validates ordinary changes; only a maintainer-pushed `vMAJOR.MINOR.PATCH` tag can publish release artifacts.

## Published Artifacts

Each release builds and verifies Linux amd64 and arm64 tarballs,
`SHA256SUMS`, and `build-metadata.json`, then publishes three multi-architecture
OCI products: `sysbox` for API/agent runtime, `sysbox-cli` containing the exact
verified host binaries, and `sysbox-metadata` containing the final runtime and
CLI digests, checksums, and license. Tarballs remain deterministic build outputs
even when Forgejo Release creation is not permitted.

For `v0.3.4`, OCI tags are:

```text
git.pku.edu.cn/oslab/sysbox:v0.3.4
git.pku.edu.cn/oslab/sysbox:0.3.4
git.pku.edu.cn/oslab/sysbox:0.3
git.pku.edu.cn/oslab/sysbox:0
git.pku.edu.cn/oslab/sysbox:latest
```

The same tag set is applied independently to `sysbox-cli` and
`sysbox-metadata`.

The workflow refuses pre-existing immutable version tags for either product and
serializes publication. Tags remain registry-mutable; `oci_digest` and
`cli_oci_digest` in runner-side metadata are the immutable identities. Consumers
must pin `image@sha256:...`.

## CI Tiers

`.forgejo/workflows/ci.yml` runs for pull requests and `main` without release secrets or privileged host access. It performs formatting, vet, full tests, focused race tests, static builds, example plans, release artifact tests, workflow contract tests, and shell syntax checks.

`.forgejo/workflows/acceptance.yml` is manually dispatched on a trusted runner. Its `tier` input selects `privileged`, `heterogeneous-matrix`, or `heterogeneous-reset`. Run all three against the exact commit intended for release and record the successful action URLs during review.

`.forgejo/workflows/release.yml` runs only for a pushed stable tag. It reruns CI
gates, creates and verifies local artifacts, then publishes and validates all
three OCI products. Metadata is published last, after the runtime and CLI
digests are final. It does not require Forgejo Release API access.

## Trusted Runner

Protected acceptance and publication require a self-hosted runner with labels `self-hosted`, `linux`, and `release`. It needs:

- Go matching `go.mod`.
- Git, Bash, GNU tar, gzip, jq, curl, sha256sum, and binutils `strings`.
- Docker Engine with Buildx and registry network access.
- KVM, Firecracker, libvirt, and host network privileges for heterogeneous acceptance.
- Enough disk for multi-architecture Docker layers and VM artifacts.

Do not attach the `release` label to runners that execute untrusted pull-request jobs.

## Forgejo Credentials

Create a dedicated repository Actions secret named `RELEASE_TOKEN`. Grant it
permission to authenticate and push packages to the `sysbox`, `sysbox-cli`, and
`sysbox-metadata` container namespaces. Forgejo Release creation permission is
not required.

The token is available only to the trusted `publish` job and is passed to
`docker login` over standard input. Never put it in command arguments, `.env`,
HCL, metadata, logs, or OCI labels.

Checked-in defaults are:

```text
OCI_IMAGE=git.pku.edu.cn/oslab/sysbox
CLI_OCI_IMAGE=git.pku.edu.cn/oslab/sysbox-cli
METADATA_OCI_IMAGE=git.pku.edu.cn/oslab/sysbox-metadata
```

Forks and alternate Forgejo installations must update the workflow environment before tagging.

## Local Dry Run

Build and compare both architectures without registry or Forgejo credentials:

```bash
make release-test
```

To inspect one untagged development build:

```bash
scripts/release/build.sh --tag v0.1.0 --output /tmp/sysbox-dist --allow-untagged
scripts/release/verify.sh /tmp/sysbox-dist
```

`--allow-untagged` is local-only. The Make release target and Forgejo workflow never use it.

## Promotion Procedure

1. Confirm `main` CI is green and the worktree is clean.
2. Run all protected acceptance tiers for the exact `main` commit.
3. Review user-visible changes and choose the next stable SemVer.
4. Create an annotated tag and push only that tag.

```bash
git switch main
git pull --ff-only
git status --short
git tag -a v0.1.0 -m "Sysbox v0.1.0"
git push origin v0.1.0
```

The workflow refuses malformed tags, dirty release builds, tags not pointing at
the checked-out commit, and pre-existing immutable version tags in either OCI
repository.

## Installing the Host CLI

Topology repositories extract final `/build-metadata.json` from the versioned
metadata image, then extract the exact CLI binaries using its recorded digest
and hashes. `sysbox-topology` automates the host installation:

```bash
docker login git.pku.edu.cn
docker pull git.pku.edu.cn/oslab/sysbox-metadata:v0.1.0
docker pull git.pku.edu.cn/oslab/sysbox-cli@sha256:<manifest-digest>
make bootstrap
```

The CLI runs on the host after extraction. OCI is the distribution carrier, not
a privileged topology executor.

## Using the OCI Image

```bash
docker pull git.pku.edu.cn/oslab/sysbox:v0.1.0
docker run --rm --entrypoint sysbox git.pku.edu.cn/oslab/sysbox:v0.1.0 version --json

SYSBOX_IMAGE=git.pku.edu.cn/oslab/sysbox:v0.1.0 docker compose \
  -f deploy/docker/compose.yml \
  -f deploy/docker/compose.agent.yml up -d
```

The image does not provide host capabilities automatically. Firecracker/libvirt/network execution still requires documented mounts, devices, privileges, and artifacts.

## Failure Inspection

The three OCI repositories cannot be published in one registry transaction.

- Local build or verification failure publishes nothing.
- A failure after an earlier OCI product succeeds leaves a partial version that
  must be inspected before retrying. Absence of the metadata image means the
  release is incomplete.
- Tooling never overwrites or automatically deletes external artifacts.

Inspect before retrying:

```bash
docker buildx imagetools inspect git.pku.edu.cn/oslab/sysbox:v0.1.0
docker buildx imagetools inspect git.pku.edu.cn/oslab/sysbox-cli:v0.1.0
docker buildx imagetools inspect git.pku.edu.cn/oslab/sysbox-metadata:v0.1.0
```

Use an administrator-approved registry operation to remove only confirmed
incomplete records. Never delete a version consumers may already use. Retry
only from the unchanged tagged commit.

## License

Source and binary distributions use the [Mulan Permissive Software License, Version 2](../LICENSE), SPDX `MulanPSL-2.0`. Every archive contains the complete license text and every OCI image carries the matching label.
