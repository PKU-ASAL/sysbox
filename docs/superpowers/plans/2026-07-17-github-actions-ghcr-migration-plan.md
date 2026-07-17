# GitHub Actions and GHCR Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace unavailable Forgejo Actions runners with GitHub-hosted CI and tag-driven GHCR publication while preserving local heterogeneous acceptance.

**Architecture:** `.github/workflows/ci.yml` runs unprivileged validation on GitHub-hosted Ubuntu runners. `.github/workflows/release.yml` uses the built-in `GITHUB_TOKEN`, QEMU, and Buildx to publish versioned runtime, CLI, and final metadata images to `ghcr.io/pku-asal`. KVM/libvirt/Firecracker acceptance remains an explicit local maintainer gate because GitHub-hosted runners cannot provide the required trusted host contract.

**Tech Stack:** GitHub Actions, GHCR, Docker Buildx/QEMU, Go, Bash, jq

## Global Constraints

- Keep Forgejo as the primary Git remote and GitHub as the CI/release mirror.
- Use only GitHub-hosted runners for checked-in workflows.
- Never expose registry credentials to pull-request jobs.
- Use `GITHUB_TOKEN` with `contents: read` and `packages: write` only in release.
- Publish runtime, CLI, then durable metadata OCI products in that order.
- Run heterogeneous acceptance locally before creating a production tag.
- Pin every third-party GitHub Action to a full commit SHA.

---

### Task 1: GitHub Workflow Contracts

**Files:**
- Modify: `scripts/release/workflowcheck/main.go`
- Modify: `scripts/release/workflowcheck/main_test.go`
- Modify: `scripts/release/workflow_test.sh`

**Interfaces:**
- Validates `.github/workflows/ci.yml` and `.github/workflows/release.yml`.

- [ ] Add failing tests for hosted runners, GHCR package permission, scoped `GITHUB_TOKEN`, QEMU/Buildx setup, and ordered three-image publication.
- [ ] Run workflow tests and confirm `.github` workflows are missing.
- [ ] Implement the minimum GitHub-specific validation.
- [ ] Rerun workflow tests.

### Task 2: Hosted CI and GHCR Release

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`
- Delete: `.forgejo/workflows/ci.yml`
- Delete: `.forgejo/workflows/acceptance.yml`
- Delete: `.forgejo/workflows/release.yml`

**Interfaces:**
- CI triggers on `main` pushes and pull requests.
- Release triggers only on `vMAJOR.MINOR.PATCH` tag pushes.
- Publishes `ghcr.io/pku-asal/sysbox`, `sysbox-cli`, and `sysbox-metadata`.

- [ ] Add workflows using full-SHA action pins and least-privilege permissions.
- [ ] Validate YAML and repository workflow contracts.
- [ ] Confirm release verification precedes all registry mutations.

### Task 3: Remove Dead Forgejo Publication and Update Docs

**Files:**
- Delete: `scripts/release/forgejo.sh`
- Modify: `README.md`, `docs/README.md`, `docs/releasing.md`, `Makefile`

**Interfaces:**
- Documents GitHub CI/GHCR and local heterogeneous acceptance.

- [ ] Remove only the now-unreachable Forgejo Release API implementation.
- [ ] Replace current operational Forgejo registry/runner instructions with GHCR instructions.
- [ ] Preserve historical specs and plans unchanged as archived decision records.

### Task 4: Verification and Delivery

**Files:**
- Modify only files required by findings.

**Interfaces:**
- Produces one reviewed commit mirrored to Forgejo and GitHub.

- [ ] Run full tests, vet, focused race, release artifact tests, workflow tests, shell syntax, and diff checks.
- [ ] Build the CLI and metadata Dockerfiles locally from verified release artifacts.
- [ ] Fix every Critical and Important review finding.
- [ ] Commit once, push `origin main`, then push `github main` without creating a release tag.
