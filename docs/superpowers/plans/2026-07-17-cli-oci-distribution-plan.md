# Sysbox CLI OCI Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a minimal multi-architecture Sysbox CLI OCI image whose immutable identity and binary hashes can initialize `sysbox-topology/sysbox.lock` without Forgejo Release permissions.

**Architecture:** Release builds remain the canonical source of per-platform
binary hashes. A scratch CLI image copies those exact outputs, and a final
scratch metadata image persists the runtime and CLI digests. The OCI script
accepts explicit Dockerfile and metadata-field inputs so all three products
share one hardened publication implementation without Forgejo Release APIs.

**Tech Stack:** Go static builds, Docker Buildx, Bash, jq, Forgejo Actions

## Global Constraints

- Publish `sysbox`, `sysbox-cli`, and `sysbox-metadata` as separate
  multi-platform OCI products, with metadata last.
- Identify consumer images with manifest digest, never mutable tags.
- Record amd64/arm64 `sysbox` and `sysbox-init` SHA256 in release metadata.
- Preserve deterministic tarballs and local verification.
- Do not require Forgejo Release creation permission for OCI publication.
- Do not create a real topology lock before a real OCI digest exists.

---

### Task 1: Release Metadata Contract

**Files:**
- Modify: `scripts/release/test.sh`
- Modify: `scripts/release/verify.sh`
- Modify: `scripts/release/build.sh`

**Interfaces:**
- Produces target fields `sysbox_sha256` and `sysbox_init_sha256` in `build-metadata.json`.

- [ ] Add failing assertions for both binary hashes on both targets.
- [ ] Run `make release-test` and confirm the fields are absent.
- [ ] Compute hashes from staged binaries and include them in top-level target metadata.
- [ ] Verify metadata hashes against extracted archive members and rerun the test.

### Task 2: Minimal CLI OCI Product

**Files:**
- Create: `Dockerfile.cli`
- Modify: `scripts/release/test.sh`
- Modify: `scripts/release/oci.sh`

**Interfaces:**
- `scripts/release/oci.sh ... --dockerfile FILE --metadata-field FIELD`
- CLI image paths `/usr/local/bin/sysbox` and `/usr/local/bin/sysbox-init`.

- [ ] Add failing static contracts for scratch runtime, required files, labels, and parameterized OCI publishing.
- [ ] Run release tests and confirm failure because `Dockerfile.cli` is absent.
- [ ] Add the minimal multi-stage image and validated OCI script options.
- [ ] Rerun release tests.

### Task 3: Dual OCI Workflow Without Forgejo Release Permission

**Files:**
- Modify: `.forgejo/workflows/release.yml`
- Modify: `scripts/release/workflowcheck/main.go`
- Modify: `scripts/release/workflowcheck/main_test.go`

**Interfaces:**
- Publishes `${OCI_IMAGE}` and `${OCI_IMAGE}-cli` and records `oci_digest` and `cli_oci_digest`.

- [ ] Add failing workflow checker tests requiring ordered runtime and CLI publication and forbidding a required Forgejo Release call.
- [ ] Run `make release-workflow-test` and confirm the old workflow fails.
- [ ] Update workflow and checker to enforce the new contract.
- [ ] Rerun workflow tests.

### Task 4: Consumer Documentation and Lock Initialization

**Files:**
- Modify: `README.md`
- Modify: `docs/releasing.md`
- Create in sibling repository: `tools/init-lock.sh`
- Modify in sibling repository: `Makefile`, `README.md`, and bootstrap tests.

**Interfaces:**
- `make init-lock METADATA=... CLI_OCI_DIGEST=sha256:...` writes a reviewed `sysbox.lock` from real release metadata.

- [ ] Add failing consumer tests for metadata-to-lock conversion.
- [ ] Implement strict conversion with no placeholder or registry lookup fallback.
- [ ] Document publishing, private registry login, extraction, and lock initialization.
- [ ] Run both repositories' verification suites.

### Task 5: Full Verification and Review

**Files:**
- Modify only files required by findings.

**Interfaces:**
- Produces reviewed commits in both independent repositories.

- [ ] Run `go test ./...`, `go vet ./...`, focused release tests, workflow tests, and focused race tests.
- [ ] Run `make verify` in `sysbox-topology` and `git diff --check` in both repositories.
- [ ] Review immutable identity, metadata consistency, workflow permissions, and failure behavior.
- [ ] Fix all Critical and Important findings, rerun verification, and commit each repository separately.
