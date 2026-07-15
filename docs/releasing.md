# Releasing Sysbox

Sysbox uses Forgejo Actions and explicit stable SemVer tags. CI validates ordinary changes; only a maintainer-pushed `vMAJOR.MINOR.PATCH` tag can publish release artifacts.

## Published Artifacts

Each release publishes Linux amd64 and arm64 tarballs, `SHA256SUMS`, `build-metadata.json`, and a multi-architecture OCI image. Each archive contains `sysbox`, `sysbox-init`, `README.md`, `LICENSE`, and architecture-specific build metadata.

For `v0.3.4`, OCI tags are:

```text
git.pku.edu.cn/oslab/sysbox:v0.3.4
git.pku.edu.cn/oslab/sysbox:0.3.4
git.pku.edu.cn/oslab/sysbox:0.3
git.pku.edu.cn/oslab/sysbox:0
git.pku.edu.cn/oslab/sysbox:latest
```

The release workflow refuses pre-existing `v0.3.4` and `0.3.4` tags and serializes publication. OCI registries do not provide a portable conditional tag-create operation, so tags remain registry-mutable; the manifest digest recorded in `build-metadata.json` is the immutable image identity. Registry write credentials must remain exclusive to the protected release workflow. Consumers requiring strict immutability should pin `git.pku.edu.cn/oslab/sysbox@sha256:...`.

## CI Tiers

`.forgejo/workflows/ci.yml` runs for pull requests and `main` without release secrets or privileged host access. It performs formatting, vet, full tests, focused race tests, static builds, example plans, release artifact tests, workflow contract tests, and shell syntax checks.

`.forgejo/workflows/acceptance.yml` is manually dispatched on a trusted runner. Its `tier` input selects `privileged`, `heterogeneous-matrix`, or `heterogeneous-reset`. Run all three against the exact commit intended for release and record the successful action URLs during review.

`.forgejo/workflows/release.yml` runs only for a pushed stable tag. It reruns CI gates, creates and verifies local artifacts, publishes and validates OCI, then creates and audits the Forgejo Release.

## Trusted Runner

Protected acceptance and publication require a self-hosted runner with labels `self-hosted`, `linux`, and `release`. It needs:

- Go matching `go.mod`.
- Git, Bash, GNU tar, gzip, jq, curl, sha256sum, and binutils `strings`.
- Docker Engine with Buildx and registry network access.
- KVM, Firecracker, libvirt, and host network privileges for heterogeneous acceptance.
- Enough disk for multi-architecture Docker layers and VM artifacts.

Do not attach the `release` label to runners that execute untrusted pull-request jobs.

## Forgejo Credentials

Create a dedicated repository Actions secret named `RELEASE_TOKEN`. Grant only permission to read repository/tag data, create releases and assets in `oslab/sysbox`, and push packages to the Sysbox container namespace.

The token is available only to the trusted `publish` job. Scripts pass authorization through a mode-0600 curl config file, not command arguments. Never put the token in `.env`, HCL, metadata, logs, or OCI labels.

Checked-in defaults are:

```text
FORGEJO_API_URL=https://git.pku.edu.cn/api/v1
FORGEJO_REPOSITORY=oslab/sysbox
OCI_IMAGE=git.pku.edu.cn/oslab/sysbox
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

The workflow refuses malformed tags, dirty release builds, tags not pointing at the checked-out commit, existing immutable OCI tags, and existing Forgejo Releases.

## Installing Binary Artifacts

Download the archive and `SHA256SUMS` from the same Forgejo Release:

```bash
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf sysbox_v0.1.0_linux_amd64.tar.gz
./sysbox version --json
sudo install -m 0755 sysbox /usr/local/bin/sysbox
sudo install -m 0755 sysbox-init /usr/local/bin/sysbox-init
```

The version JSON must match the release tag, commit, and top-level build metadata.

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

Registry and Forgejo Release publication cannot be one transaction.

- Local build or verification failure publishes nothing.
- OCI failure prevents Forgejo Release creation.
- Release creation or asset upload failure may leave an incomplete Forgejo Release.
- Tooling never overwrites or automatically deletes external artifacts.

Inspect before retrying:

```bash
RELEASE_TOKEN=... \
FORGEJO_API_URL=https://git.pku.edu.cn/api/v1 \
FORGEJO_REPOSITORY=oslab/sysbox \
scripts/release/forgejo.sh audit --tag v0.1.0 --dist dist

docker buildx imagetools inspect git.pku.edu.cn/oslab/sysbox:v0.1.0
```

Use the Forgejo UI or an administrator-approved API operation to remove only confirmed incomplete records. Never delete a stable release that users may already consume. Retry only from the unchanged tagged commit.

## License

Source and binary distributions use the [Mulan Permissive Software License, Version 2](../LICENSE), SPDX `MulanPSL-2.0`. Every archive contains the complete license text and every OCI image carries the matching label.
