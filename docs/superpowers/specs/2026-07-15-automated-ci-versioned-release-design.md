# Automated CI and Versioned Release Design

Date: 2026-07-15

## Goal

Create a Forgejo-native CI and release system that continuously validates Sysbox and publishes one coherent, versioned set of CLI, guest-init, container, checksum, and build-metadata artifacts from an explicitly approved SemVer tag.

## Release Authority

The Git tag is the sole version authority. A release starts only when a tag matching `vMAJOR.MINOR.PATCH` is pushed. The first intended release line is `v0.x`; selecting the exact first version remains an explicit maintainer action rather than a workflow decision.

The workflow rejects prerelease, build-metadata, malformed, or non-canonical tags in the first implementation. Supporting `vMAJOR.MINOR.PATCH-rc.N` can be added later with an explicit channel and floating-tag policy.

Normal pushes to `main` and pull requests run CI but never create tags, releases, or registry artifacts. The workflow does not infer versions from commit messages.

## Forgejo Runner Assumptions

The release runner is a trusted, self-hosted Linux runner with:

- Docker Engine and Buildx.
- Network access to the Forgejo API and Container Registry.
- A release token scoped to the Sysbox repository and package publication.
- Enough storage for Go build caches and multi-architecture image layers.

Every required tool and capability is checked before building. Missing Docker, Buildx, registry authentication, API access, or upload permission fails the release before public artifacts are created.

Untrusted pull-request jobs do not receive release tokens, registry credentials, Docker socket access, KVM, libvirt, or host network administration privileges.

## Continuous Integration Workflow

`.forgejo/workflows/ci.yml` runs for pull requests and pushes to `main`. It performs:

1. Repository checkout with full enough history for build metadata.
2. Go toolchain setup using the version declared by `go.mod`.
3. Formatting and generated-file cleanliness checks.
4. `go vet ./...`.
5. `go test ./...`.
6. Focused race tests for runtime, state, Docker, Firecracker, and libvirt packages.
7. A static Linux build of `sysbox` and both supported `sysbox-init` architectures.
8. Plans for checked-in example topologies through the existing `make ci` contract.
9. Shell syntax checks for repository release and acceptance scripts.

Privileged Docker, KVM, Firecracker, libvirt, and heterogeneous acceptance tests do not run in untrusted pull-request CI. They remain explicit protected-runner jobs and are required by release policy before tagging, but they do not grant host privilege to arbitrary pull-request code.

## Protected Acceptance Workflow

`.forgejo/workflows/acceptance.yml` is manually dispatched on a trusted runner. It exposes explicit inputs for the acceptance tier:

- `privileged`: Docker/network ownership and recovery tests.
- `heterogeneous-matrix`: six directed IPv4 communication paths and residue audit.
- `heterogeneous-reset`: three full and three targeted reset cycles.

The workflow calls the existing Make targets rather than duplicating topology mechanics. Maintainers run the full protected acceptance tier against the commit that will receive a release tag. The resulting Forgejo action URL and commit SHA are recorded in the release notes or build metadata.

The first implementation does not automatically tag after acceptance. Human review remains the promotion gate.

## Version Injection

Sysbox gains a `version` command backed by a small build-information package. Development builds report:

- version: `dev`
- commit: `unknown` when VCS metadata is unavailable
- build time: `unknown` when not injected
- Go runtime version

Release builds inject version, full commit SHA, and UTC build time through `-ldflags`. The command supports stable human-readable output and structured JSON so scripts can verify that a binary matches its release metadata.

Provider protocol versions, state schema v6, API schema versions, and release version remain separate contracts. A product release does not silently rewrite any persistent schema version.

## Reproducible Binary Artifacts

Repository-owned scripts under `scripts/release/` implement the release mechanics and can run outside Forgejo Actions. The scripts:

- Validate canonical SemVer tags.
- Refuse a dirty tracked worktree for a release build.
- Verify the tag points at `HEAD`.
- Derive `SOURCE_DATE_EPOCH` from the tagged commit timestamp.
- Build with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and explicit version ldflags.
- Build `sysbox` and `sysbox-init` for Linux amd64 and arm64.
- Normalize archive ownership, ordering, modes, and timestamps.

Each architecture produces one archive:

```text
sysbox_vMAJOR.MINOR.PATCH_linux_amd64.tar.gz
sysbox_vMAJOR.MINOR.PATCH_linux_arm64.tar.gz
```

Each archive contains:

```text
sysbox
sysbox-init
README.md
LICENSE
build-metadata.json
```

The release directory also contains `SHA256SUMS` covering both archives and a top-level `build-metadata.json` describing version, tag, commit, commit timestamp, build time, Go version, targets, archive names, checksums, license identifier, source repository, and OCI image reference.

The reproducibility contract applies when the same source commit, Go toolchain, base files, and release inputs are used. Container image byte identity is not promised across different Buildx or registry implementations; OCI source/version/revision labels and immutable version tags provide traceability.

## OCI Image Publication

The existing Dockerfile becomes version-aware through build arguments and OCI labels. A release publishes one multi-architecture Linux image manifest for amd64 and arm64 to the Forgejo Container Registry.

The registry repository defaults to the canonical Forgejo project package path and is configurable through a workflow environment variable so the installation can match its registry naming rules.

For `v0.3.4`, the workflow publishes:

```text
v0.3.4
0.3.4
0.3
0
latest
```

`v0.3.4` and `0.3.4` are protected release tags: the workflow refuses them when they already exist, serializes release jobs, validates the first version tag before promotion, and verifies that every promoted tag resolves to the same manifest. OCI registries do not expose a portable atomic create-if-absent operation, so the manifest digest recorded in release metadata is the actual immutable identity. Registry write credentials are exclusive to the protected release workflow. Floating tags are updated only after the first version tag validates. Prerelease tags are outside the first implementation and never affect floating tags.

The OCI image carries standard labels for title, description, source, revision, version, creation time, licenses (`MulanPSL-2.0`), and documentation URL. The image continues to support `sysbox serve` as its default entrypoint and can run Agent commands by overriding the container command.

## Forgejo Release Publication

The workflow builds and validates every local artifact before creating the Forgejo Release. It then:

1. Authenticates to the Container Registry.
2. Verifies immutable OCI tags do not already exist.
3. Pushes the multi-architecture OCI manifest and validates its architectures and labels.
4. Creates a non-draft, non-prerelease Forgejo Release for the exact Git tag.
5. Uploads both archives, `SHA256SUMS`, and top-level `build-metadata.json`.
6. Reads the release assets back through the Forgejo API and verifies names and checksums.

The release job never overwrites an existing Forgejo Release. Retrying after a partial external failure requires a maintainer to inspect and remove incomplete external artifacts before rerunning; the scripts provide an audit command but do not automatically delete published packages or releases.

Forgejo API calls are explicit repository-owned scripts using documented REST endpoints. Tokens are passed through environment variables, never command arguments, archives, metadata, logs, or OCI labels.

## Failure and Atomicity Model

True cross-service atomicity is impossible because the Forgejo Release API and Container Registry are separate stores. The design minimizes partial publication by completing all local work first and publishing immutable OCI content before creating the visible Forgejo Release.

Failure rules are deterministic:

- CI failure prevents all publication.
- Invalid version, dirty tree, tag mismatch, or missing tools fails before build.
- Local artifact or checksum failure fails before registry/API access.
- OCI push or validation failure prevents Forgejo Release creation.
- Release creation or asset upload failure leaves an auditable partial release and fails loudly.
- Existing protected OCI tags or an existing Forgejo Release cause preflight refusal. The exclusive registry credential and serialized workflow are operational requirements because registry tags are intrinsically mutable.
- Floating tags update only after immutable OCI publication succeeds.

The release documentation includes manual inspection and cleanup procedures for the remaining partial-release case.

## License

The repository adopts the official Mulan Permissive Software License, Version 2 (`MulanPSL-2.0`). A root `LICENSE` contains the official license text. Binary archives include the same file, build metadata and OCI labels use the SPDX identifier, and README documentation states the project license.

No workflow may generate, abbreviate, or paraphrase the legal text. The implementation must source the exact official Mulan PSL v2 text and review it as a standalone repository change.

## Secrets and Permissions

The release workflow uses a dedicated secret such as `RELEASE_TOKEN`. Its minimum permissions are:

- Read repository and tag data.
- Create a release and upload assets in the Sysbox repository.
- Push packages to the Sysbox container namespace.

CI jobs use no release secret. Release tokens are unavailable to pull-request workflows. Workflow permissions and runner labels are explicit. Shell tracing is disabled around authentication, and scripts reject missing token/registry variables without printing values.

## Testing

Release support includes automated tests for:

- Valid and invalid canonical SemVer tags.
- Dirty worktree and tag-to-HEAD mismatch rejection.
- Development and injected `sysbox version` output, including JSON.
- Archive file names, members, modes, and normalized timestamps.
- Metadata schema and correspondence with binary version output.
- SHA256SUMS coverage and verification.
- Dockerfile OCI label arguments.
- Workflow trigger and permission invariants through static checks.

A local dry run builds both architecture archives without Forgejo credentials or registry push. A release preflight mode checks the Forgejo API and registry without creating a release. The final acceptance uses a temporary SemVer tag in a controlled repository or staging namespace before enabling production tag publication.

## Documentation

The repository documents:

- CI and protected acceptance tiers.
- Trusted runner prerequisites and labels.
- Required Forgejo secret and package permissions.
- The tag-driven release procedure.
- Artifact names and checksum verification.
- OCI pull and Compose version pinning.
- Version inspection through `sysbox version`.
- Failure inspection and partial-publication cleanup.
- The MulanPSL-2.0 license.

## Scope

The first implementation does not include automatic semantic-version calculation, prereleases, nightly builds, Windows or macOS binaries, Homebrew/APT/RPM packages, SBOM generation, vulnerability scanning, GPG signing, Cosign signing, SLSA provenance, automatic changelog generation, or automatic release promotion after privileged acceptance.

These capabilities can be layered onto the stable tag, metadata, checksum, and OCI contracts without changing the first release format.
