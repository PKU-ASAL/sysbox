# Releasing Sysbox

Sysbox uses GitHub Actions and explicit stable SemVer tags. Ordinary pushes and
pull requests run hosted CI. Only a maintainer-pushed `vMAJOR.MINOR.PATCH` tag
can publish versioned OCI products.

## Published Products

Every release builds and verifies deterministic Linux amd64/arm64 tarballs,
`SHA256SUMS`, and `build-metadata.json`, then publishes three multi-architecture
images:

- `ghcr.io/pku-asal/sysbox` is the API/agent runtime.
- `ghcr.io/pku-asal/sysbox-cli` contains the exact verified host binaries.
- `ghcr.io/pku-asal/sysbox-metadata` contains the final runtime and CLI digests,
  binary/archive checksums, build identity, and license.

For `v0.3.4`, all three repositories receive `v0.3.4`, `0.3.4`, `0.3`, `0`,
and `latest` tags. Tags are convenient selectors; consumers must pin the
manifest digest recorded in metadata.

## CI and Acceptance

`.github/workflows/ci.yml` runs formatting, vet, full tests, focused race tests,
static builds, example plans, artifact tests, workflow contracts, and shell
syntax on GitHub-hosted Ubuntu runners. It has read-only repository permission
and no package or release secret.

Real heterogeneous acceptance remains local because GitHub-hosted runners do
not provide the required trusted KVM, Firecracker, libvirt, and host-network
contract. Before tagging the exact release commit, run on the acceptance host:

```bash
make test-privileged-container
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

Record the commit and results in the release review. Do not expose this host to
pull-request workflows.

## GitHub Permissions

`.github/workflows/release.yml` defaults to `contents: read`; only its publish
job receives `packages: write`. It logs into GHCR with the built-in
`GITHUB_TOKEN`, so no personal registry token is required.

In GitHub repository settings, Actions workflow permissions must allow the
workflow to write packages. Published packages should be made public when
anonymous topology bootstrap is required.

## Local Dry Run

Build and compare both architectures without registry credentials:

```bash
make release-test
```

Inspect one untagged development build:

```bash
scripts/release/build.sh --tag v0.1.0 --output /tmp/sysbox-dist --allow-untagged
scripts/release/verify.sh /tmp/sysbox-dist
```

`--allow-untagged` is local-only. The tag workflow never uses it.

## Promotion Procedure

1. Confirm GitHub CI is green and both Git remotes contain the release commit.
2. Run all three local heterogeneous acceptance commands.
3. Confirm the worktree is clean and choose the next stable SemVer.
4. Create one annotated tag and push the same tag to Forgejo and GitHub.

```bash
git switch main
git pull --ff-only origin main
git status --short
git tag -a v0.1.0 -m "Sysbox v0.1.0"
git push origin v0.1.0
git push github v0.1.0
```

The GitHub tag push triggers release. The workflow refuses malformed tags, tags
not pointing at the checked-out commit, dirty builds, and pre-existing immutable
version tags in any of the three GHCR repositories.

## Publication Order

The release workflow:

1. Repeats full CI verification.
2. Configures QEMU and Docker Buildx.
3. Builds deterministic binaries and verifies all hashes.
4. Preflights all three GHCR version tags.
5. Publishes and verifies runtime OCI.
6. Publishes and verifies CLI OCI.
7. Publishes metadata OCI last, after both consumer digests are final.

Absence of the metadata image means the release is incomplete.

## Topology Bootstrap

Extract `/build-metadata.json` from the versioned metadata image, generate the
topology lock, then let `sysbox-topology` extract and verify the host CLI:

```bash
docker pull ghcr.io/pku-asal/sysbox-metadata:v0.1.0
docker create --name sysbox-metadata-v0.1.0 \
  ghcr.io/pku-asal/sysbox-metadata:v0.1.0
docker cp sysbox-metadata-v0.1.0:/build-metadata.json \
  /tmp/sysbox-v0.1.0-build-metadata.json
docker rm sysbox-metadata-v0.1.0

cd ../sysbox-topology
make init-lock METADATA=/tmp/sysbox-v0.1.0-build-metadata.json
make bootstrap
make sysbox-version
```

The extracted CLI executes on the host. OCI is the immutable distribution
carrier; it does not replace host Docker, `/dev/kvm`, Firecracker, libvirt,
network privileges, kernels, rootfs files, or qcow2 images.

## Failure Inspection

Three OCI repositories cannot be updated transactionally. A later failure can
leave an earlier product published. The workflow never overwrites or deletes
external artifacts automatically.

Inspect all products before an administrator-approved cleanup or retry:

```bash
docker buildx imagetools inspect ghcr.io/pku-asal/sysbox:v0.1.0
docker buildx imagetools inspect ghcr.io/pku-asal/sysbox-cli:v0.1.0
docker buildx imagetools inspect ghcr.io/pku-asal/sysbox-metadata:v0.1.0
```

Retry only from the unchanged tagged commit. Never delete a version consumers
may already use.

## License

Source and published products use the
[Mulan Permissive Software License, Version 2](../LICENSE), SPDX
`MulanPSL-2.0`.
