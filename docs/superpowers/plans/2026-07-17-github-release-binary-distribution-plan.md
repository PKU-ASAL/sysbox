# GitHub Release Binary Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace CLI/metadata OCI transport with verified GitHub Release assets while retaining one GHCR runtime image.

**Architecture:** The deterministic release build produces amd64/arm64 archives, checksums, and metadata. Release publishes and verifies the runtime OCI first, writes its digest into metadata, then creates one immutable GitHub Release and audits downloaded assets. `sysbox-topology` schema v2 selects a per-platform Release archive, verifies archive and binary hashes, and activates the extracted CLI on the host.

**Tech Stack:** GitHub Actions/Releases, GHCR, Go, Bash, jq, curl, tar, SHA256

## Global Constraints

- Publish only `ghcr.io/pku-asal/sysbox` to GHCR.
- Publish two architecture archives, `SHA256SUMS`, and `build-metadata.json` to one GitHub Release.
- Refuse existing version tags, existing GitHub Releases, hash mismatches, and asset-set drift.
- Use topology lock schema v2 only; reject schema v1 immediately.
- Never fall back to a system `sysbox` or an unverified download.
- Preserve atomic host-tool activation and existing-install immutability.

---

### Task 1: Release Asset Contracts

**Files:** `scripts/release/test.sh`, `scripts/release/github.sh`, `.github/workflows/release.yml`, workflow checker tests.

- [ ] Add failing tests for the four exact Release assets and one runtime OCI publication.
- [ ] Implement GitHub Release preflight, publish, download, and audit using `gh` plus `GITHUB_TOKEN`.
- [ ] Remove CLI/metadata OCI workflow steps and require `contents: write` only on publish.
- [ ] Run release and workflow contract tests.

### Task 2: Simplify Build and OCI Contracts

**Files:** `scripts/release/build.sh`, `verify.sh`, `oci.sh`, `Dockerfile.cli`, `Dockerfile.metadata`.

- [ ] Remove OCI-only staged binaries and CLI/metadata image fields.
- [ ] Keep archive and per-binary hashes in top-level metadata and add `release_repository`.
- [ ] Simplify OCI publication to the canonical runtime Dockerfile and `oci_digest` field.
- [ ] Delete both obsolete Dockerfiles and verify no live reference remains.

### Task 3: Topology Lock Schema v2 and Bootstrap

**Files:** sibling `sysbox-topology` lock example, init/bootstrap scripts, tests, Makefile, README.

- [ ] Write failing tests for archive download, archive SHA, extraction, binary hashes, identity, failure recovery, and schema v1 rejection.
- [ ] Generate schema v2 locks from release metadata with exact per-target asset names and hashes.
- [ ] Download through curl, verify before extraction, and atomically activate the host tools.
- [ ] Remove Docker as a bootstrap dependency and reject all old lock structures.

### Task 4: Documentation, Verification, and Delivery

- [ ] Update current release and topology docs; preserve historical decision records.
- [ ] Run full tests, vet, focused race, release/workflow tests, topology verify, shell syntax, and diff checks.
- [ ] Review and fix every Critical/Important finding.
- [ ] Commit each repository, push all configured remotes, and wait for hosted CI success without creating a production tag.
