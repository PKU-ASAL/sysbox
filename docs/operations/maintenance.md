# Maintenance And Releases

## State And Workspace Maintenance

- Back up state, checkpoint and API workspace storage before upgrading.
- Wait for active runs and Agent command leases to finish.
- Verify the target release's state schema before starting a new binary.
- Never hand-edit provider-private state or remove checkpoints to force an apply.
- For Postgres, back up the database and verify advisory lock/CAS behavior after restore.
- For disposable local Compose environments, `make api clean` removes the database volume and API workspaces; it is destructive.

Sysbox rejects unsupported old state instead of guessing ownership or guest identity. Destroy old topologies with the old binary when a release documents a hard schema break.

## Agent Upgrade

Quarantine or drain the Agent, confirm no claimed run remains, preserve its workspace, deploy the matching API/Agent protocol version, then verify heartbeat, capabilities and inventory before re-enabling scheduling.

## Releasing Sysbox

Sysbox uses GitHub Actions and stable `vMAJOR.MINOR.PATCH` tags. Ordinary pushes
and pull requests run hosted CI. A release tag publishes one runtime image and
one GitHub Release containing host binaries.

## Distribution Model

GHCR contains only the API/agent runtime:

```text
ghcr.io/pku-asal/sysbox
```

Each GitHub Release contains exactly:

```text
sysbox_vMAJOR.MINOR.PATCH_linux_amd64.tar.gz
sysbox_vMAJOR.MINOR.PATCH_linux_arm64.tar.gz
SHA256SUMS
build-metadata.json
```

Each archive contains `sysbox`, `sysbox-init`, `README.md`, `LICENSE`, and
platform build metadata. Top-level metadata records archive hashes, binary
hashes, source commit, build time, release repository, runtime image, and final
runtime manifest digest.

## CI and Local Acceptance

`.github/workflows/ci.yml` runs formatting, vet, full tests, focused race tests,
build/example plans, deterministic artifact tests, workflow contracts, and
shell syntax on GitHub-hosted Ubuntu runners.

GitHub-hosted runners cannot provide the trusted KVM, Firecracker, libvirt, and
host-network contract. Before tagging the exact release commit, run locally:

```bash
make test-privileged-container
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

Record the commit and results. Do not expose this host to pull-request jobs.

## GitHub Permissions

The workflow defaults to `contents: read`. Only the publish job receives:

```yaml
permissions:
  contents: write
  packages: write
```

It uses the built-in `GITHUB_TOKEN` for GHCR and GitHub Releases. No personal
token is required. Make the runtime package public for anonymous pulls.

## Local Dry Run

```bash
make release-test

scripts/release/build.sh --tag v0.1.0 \
  --output /tmp/sysbox-dist --allow-untagged
scripts/release/verify.sh /tmp/sysbox-dist
```

`--allow-untagged` is local-only.

## Promotion

1. Confirm hosted CI is green and both Git remotes contain the release commit.
2. Complete all three local acceptance commands.
3. Confirm the worktree is clean.
4. Create one annotated tag and push it to Forgejo and GitHub.

```bash
git switch main
git pull --ff-only origin main
git status --short
git tag -a v0.1.0 -m "Sysbox v0.1.0"
git push origin v0.1.0
git push github v0.1.0
```

The GitHub tag push triggers publication.

## Publication Order

The release job:

1. Repeats full CI verification.
2. Preflights the runtime OCI version tags and GitHub Release absence.
3. Builds and verifies deterministic archives and hashes.
4. Publishes and verifies `ghcr.io/pku-asal/sysbox`.
5. Writes the runtime manifest digest into `build-metadata.json`.
6. Creates the GitHub Release with the exact four assets.
7. Downloads every asset and compares it byte-for-byte with local output.

The GitHub Release is the visible completion marker. If runtime OCI exists but
the Release does not, publication is incomplete and must be inspected before
retrying. Tooling never overwrites or automatically deletes external artifacts.

## Topology Bootstrap

Download the final metadata asset and generate the committed topology lock:

```bash
curl -fLO https://github.com/PKU-ASAL/sysbox/releases/download/v0.1.0/build-metadata.json

cd ../sysbox-topology
make init-lock METADATA=/path/to/build-metadata.json
make bootstrap
make sysbox-version
```

Bootstrap selects the host architecture, downloads the matching archive, and
verifies the archive hash, both binary hashes, version, and source commit before
atomically activating `.tools/bin/sysbox`.

The CLI then executes on the host. Distribution does not provide Docker,
`/dev/kvm`, Firecracker, libvirt, network privileges, kernels, rootfs files, or
qcow2 images; complex heterogeneous ranges require those host capabilities and
topology artifacts.

## Failure Inspection

```bash
docker buildx imagetools inspect ghcr.io/pku-asal/sysbox:v0.1.0
gh release view v0.1.0 --repo PKU-ASAL/sysbox
```

Retry only from the unchanged tagged commit. Never delete a version consumers
may already use.

## License

Source and distributions use [MulanPSL-2.0](../../LICENSE).
