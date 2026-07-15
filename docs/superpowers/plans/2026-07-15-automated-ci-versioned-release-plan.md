# Automated CI and Versioned Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Forgejo CI and tag-driven releases that publish versioned Linux amd64/arm64 archives, checksums, metadata, and a multi-architecture OCI image.

**Architecture:** Product version data lives in a small Go package and is injected only by release builds. Repository-owned shell scripts create deterministic local artifacts and expose preflight/publish boundaries; thin Forgejo workflows select triggers, runner trust, credentials, and upload order. Protected acceptance remains manually dispatched and never runs for untrusted pull requests.

**Tech Stack:** Go 1.26.1, Cobra, POSIX/Bash shell, GNU tar/gzip, Docker Buildx, Forgejo Actions and REST API, MulanPSL-2.0.

## Global Constraints

- Only canonical stable tags matching `vMAJOR.MINOR.PATCH` may publish.
- Never expose release or registry credentials to pull-request jobs.
- Release binaries target `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0`.
- Archives contain `sysbox`, `sysbox-init`, `README.md`, `LICENSE`, and `build-metadata.json`.
- `SHA256SUMS` covers both architecture archives.
- Immutable OCI tags are `vMAJOR.MINOR.PATCH` and `MAJOR.MINOR.PATCH`; floating tags are `MAJOR.MINOR`, `MAJOR`, and `latest`.
- Product version, state schema v6, API schema, Agent protocol, and provider versions stay independent.
- License text must be the official Mulan Permissive Software License v2 with SPDX identifier `MulanPSL-2.0`.
- No prereleases, nightly builds, non-Linux binaries, package-manager formats, SBOM, vulnerability scan, GPG, Cosign, SLSA, or automatic version calculation in this batch.

---

### Task 1: License Contract

**Files:**
- Create: `LICENSE`
- Modify: `README.md`

**Interfaces:**
- Produces: the exact legal file embedded in every release archive and referenced by OCI/build metadata as `MulanPSL-2.0`.

- [x] **Step 1: Obtain and verify the official license text**

Fetch the official Mulan PSL v2 text from the authoritative Mulan/OpenEuler source over HTTPS. Verify the title contains `Mulan Permissive Software License，Version 2` or the official bilingual equivalent and compare the downloaded bytes with the authoritative source before adding them. Do not synthesize legal text.

- [x] **Step 2: Add the root license and README declaration**

Add the exact text as `LICENSE`. Add a `## License` section to `README.md` linking `LICENSE` and naming the SPDX identifier `MulanPSL-2.0`.

- [x] **Step 3: Verify and commit**

Run:

```bash
test -s LICENSE
rg -n 'Mulan|MulanPSL-2.0' LICENSE README.md
git diff --check
```

Commit:

```bash
git add LICENSE README.md
git commit -m "docs: adopt MulanPSL-2.0 license"
```

---

### Task 2: Product Build Information and Version Command

**Files:**
- Create: `pkg/buildinfo/buildinfo.go`
- Create: `pkg/buildinfo/buildinfo_test.go`
- Create: `cmd/sysbox/commands/version_cmd.go`
- Create: `cmd/sysbox/commands/version_cmd_test.go`
- Modify: `cmd/sysbox/commands/root.go`

**Interfaces:**
- Produces: `buildinfo.Info`, `buildinfo.Current() Info`, stable text output, and `sysbox version --json`.
- Consumed by: release scripts, metadata verification, and Docker image labels.

- [x] **Step 1: Write failing buildinfo tests**

Test development defaults and injected values without changing runtime globals permanently:

```go
func TestCurrentReturnsDevelopmentDefaults(t *testing.T)
func TestInfoJSONUsesStableFieldNames(t *testing.T)
```

The JSON fields are `version`, `commit`, `build_time`, and `go_version`.

- [x] **Step 2: Run RED test**

```bash
go test ./pkg/buildinfo
```

Expected: package or symbols do not exist.

- [x] **Step 3: Implement buildinfo**

Define ldflag variables `Version = "dev"`, `Commit = "unknown"`, and `BuildTime = "unknown"`. `Current()` returns those values plus `runtime.Version()`.

- [x] **Step 4: Write failing Cobra command tests**

Register `versionCmd` under the root command and assert:

```text
sysbox dev
commit: unknown
build time: unknown
go: go1...
```

For `--json`, decode the result into `buildinfo.Info` and compare it with `buildinfo.Current()`.

- [x] **Step 5: Implement and verify the command**

Use `json.NewEncoder(cmd.OutOrStdout())` for JSON and `fmt.Fprintf` for stable text. Run:

```bash
go test ./pkg/buildinfo ./cmd/sysbox/commands
go build -ldflags "-X github.com/oslab/sysbox/pkg/buildinfo.Version=v0.1.0 -X github.com/oslab/sysbox/pkg/buildinfo.Commit=0123456789abcdef -X github.com/oslab/sysbox/pkg/buildinfo.BuildTime=2026-07-15T00:00:00Z" -o /tmp/sysbox-version ./cmd/sysbox
/tmp/sysbox-version version --json
```

Expected: JSON contains the three injected values and a Go version.

- [x] **Step 6: Commit**

```bash
git add pkg/buildinfo cmd/sysbox/commands
git commit -m "feat(cli): expose release build information"
```

---

### Task 3: Deterministic Release Artifacts

**Files:**
- Create: `scripts/release/lib.sh`
- Create: `scripts/release/build.sh`
- Create: `scripts/release/verify.sh`
- Create: `scripts/release/test.sh`
- Modify: `.gitignore`
- Modify: `Makefile`

**Interfaces:**
- Consumes: canonical tag, tagged Git commit, `pkg/buildinfo` ldflag variables, root README and LICENSE.
- Produces: `dist/sysbox_<tag>_linux_<arch>.tar.gz`, `dist/SHA256SUMS`, and `dist/build-metadata.json`.

- [x] **Step 1: Write failing shell contract tests**

`scripts/release/test.sh` must create temporary Git repositories or isolated worktrees and assert:

- `validate_version v0.1.0` succeeds.
- `v1`, `1.2.3`, `v1.2.3-rc.1`, and `v01.2.3` fail.
- dirty tracked worktree fails release validation.
- tag not pointing at HEAD fails release validation.
- a dry-run archive has exactly five required members.
- metadata, binary JSON, archive name, and checksum agree.
- a second build with identical inputs produces identical archive hashes.

- [x] **Step 2: Run RED test**

```bash
bash scripts/release/test.sh
```

Expected: release helpers do not exist.

- [x] **Step 3: Implement shared validation and deterministic build**

`lib.sh` owns canonical SemVer parsing, repository checks, RFC3339 commit time, ldflags, target names, and safe required-variable helpers. `build.sh` accepts `--tag`, `--output`, and `--allow-untagged` for local dry runs. It uses sorted GNU tar entries, numeric owner/group zero, normalized modes/timestamps, and `gzip -n`.

The per-archive metadata is generated before archiving. The top-level metadata is generated after checksums and includes a target array with archive/checksum pairs.

- [x] **Step 4: Implement artifact verification**

`verify.sh` runs `sha256sum -c`, extracts each archive, checks exact members/modes, runs `sysbox version --json`, and compares version/commit/build time/license/architecture against metadata using `jq`.

- [x] **Step 5: Add Make targets and verify GREEN**

Add:

```make
release-test:
	bash scripts/release/test.sh

release-build:
	bash scripts/release/build.sh --tag $(VERSION) --output dist

release-verify:
	bash scripts/release/verify.sh dist
```

Ignore `/dist/`. Run `make release-test`, a local `release-build` against a temporary tag, and `make release-verify`.

- [x] **Step 6: Commit**

```bash
git add .gitignore Makefile scripts/release
git commit -m "feat(release): build deterministic release archives"
```

---

### Task 4: Versioned OCI Image

**Files:**
- Modify: `Dockerfile`
- Modify: `deploy/docker/compose.yml`
- Modify: `deploy/docker/compose.agent.yml`
- Create: `scripts/release/oci.sh`
- Extend: `scripts/release/test.sh`

**Interfaces:**
- Consumes: tag/commit/build time from release helpers and Forgejo registry repository.
- Produces: versioned multi-architecture image tags and verified OCI labels.

- [x] **Step 1: Add failing static OCI contract tests**

Assert Dockerfile declares `VERSION`, `REVISION`, `CREATED`, and `SOURCE_URL`, passes release ldflags to both Go builds, and sets OCI title/source/revision/version/created/licenses/documentation labels. Assert Compose images use `${SYSBOX_IMAGE:-sysbox:latest}`.

- [x] **Step 2: Make Dockerfile and Compose version-aware**

Use build args in the builder and final stages. Preserve `ENTRYPOINT ["sysbox", "serve"]`. Apply `MulanPSL-2.0` and source/version/revision labels. Both API and Agent compose services consume the same image variable.

- [x] **Step 3: Implement OCI preflight and publication**

`oci.sh` supports `preflight`, `build`, and `verify`. It validates Docker/Buildx, computes immutable/floating tags from canonical SemVer, refuses existing immutable tags using registry manifest inspection, runs `docker buildx build --platform linux/amd64,linux/arm64 --push`, and verifies manifest architectures and labels.

- [x] **Step 4: Verify and commit**

Run shell tests, `docker build` for the native architecture with injected labels, and inspect `sysbox version --json` inside the image. Commit:

```bash
git add Dockerfile deploy/docker scripts/release
git commit -m "feat(release): publish versioned OCI images"
```

---

### Task 5: Forgejo Workflows and Release API

**Files:**
- Create: `.forgejo/workflows/ci.yml`
- Create: `.forgejo/workflows/acceptance.yml`
- Create: `.forgejo/workflows/release.yml`
- Create: `scripts/release/forgejo.sh`
- Create: `scripts/release/workflow_test.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: release scripts, `RELEASE_TOKEN`, Forgejo repository/API variables, registry credentials, trusted runner labels.
- Produces: CI status, manually dispatched protected acceptance, OCI publication, Forgejo Release and four uploaded assets.

- [x] **Step 1: Write failing workflow invariant tests**

Use a structured YAML parser available through a small Go test/tool or `yq`; do not test security properties with substring-only matching. Assert:

- CI triggers only pull requests and main pushes and references no release secrets.
- acceptance triggers only `workflow_dispatch` and uses a trusted label.
- release triggers only `v*.*.*` tags, has no pull-request trigger, and references `RELEASE_TOKEN` only in the publish job.
- release calls local build/verify/OCI/Forgejo scripts in the required order.

- [x] **Step 2: Implement Forgejo REST publication script**

`forgejo.sh` supports `preflight`, `publish`, and `audit`. It reads token only from `RELEASE_TOKEN`, verifies repository/tag/release state, creates the stable release, uploads the two archives plus `SHA256SUMS` and `build-metadata.json`, then lists assets and compares downloaded hashes. It never deletes or overwrites releases.

- [x] **Step 3: Implement thin workflows**

CI runs format cleanliness, vet, full tests, focused race, `make ci`, release shell tests, and script syntax checks. Acceptance maps dispatch input to existing Make targets on the trusted runner. Release reruns CI gates, builds/verifies local artifacts, logs into the registry without echoing credentials, publishes/verifies OCI, then publishes/audits the Forgejo release.

- [x] **Step 4: Add workflow test target and verify**

Add `release-workflow-test` and include it in the release validation path. Run shell/YAML tests and inspect workflows with the local Forgejo Actions runner parser if available.

- [x] **Step 5: Commit**

```bash
git add .forgejo Makefile scripts/release
git commit -m "ci: add Forgejo validation and release workflows"
```

---

### Task 6: Release Documentation and Final Acceptance

**Files:**
- Create: `docs/releasing.md`
- Modify: `README.md`
- Modify: `docs/README.md`
- Modify: `.env.example`
- Modify: `docs/superpowers/plans/2026-07-15-automated-ci-versioned-release-plan.md`

**Interfaces:**
- Consumes: final workflow names, variables, artifact names, image tags, and failure behavior.
- Produces: maintainer and user release instructions.

- [x] **Step 1: Document runner and credential setup**

Document trusted runner labels, Docker/Buildx, registry/API URLs, `RELEASE_TOKEN` permissions, protected acceptance, manual tag promotion, OCI pull, archive checksum verification, `sysbox version`, Compose image pinning, and partial-publication audit/cleanup.

- [x] **Step 2: Document license and installation paths**

Link MulanPSL-2.0, release tarballs, Forgejo Release assets, and OCI image usage from README and docs index. Add only non-secret registry/repository defaults to `.env.example`; never add token placeholders that encourage committing credentials.

- [x] **Step 3: Run final gates**

```bash
gofmt -l .
go vet ./...
go test ./...
CGO_ENABLED=1 go test -race ./pkg/buildinfo ./cmd/sysbox/commands ./pkg/runtime ./pkg/state ./pkg/provider/docker ./pkg/provider/libvirt ./pkg/provider/firecracker
make ci
make release-test
make release-workflow-test
git diff --check
```

Build and verify both local release archives twice and compare SHA256SUMS. Build a native Docker image with release args and verify binary JSON plus OCI labels.

- [x] **Step 4: Request final code review**

Review version correctness, reproducibility, workflow trigger/secret isolation, immutable-tag protection, Forgejo API failure behavior, archive/license contents, OCI labels, and documentation. Fix every Critical and Important issue and rerun affected gates.

- [x] **Step 5: Commit**

```bash
git add README.md docs .env.example
git commit -m "docs(release): document versioned distribution"
```

- [ ] **Step 6: Staging acceptance**

Push the implementation commits without a production SemVer tag. Run CI and protected acceptance on Forgejo. Then create a temporary SemVer tag in an isolated staging repository or package namespace, verify all release assets and the multi-architecture OCI manifest, and delete only the staging namespace after recording evidence. Production tagging remains a separate maintainer decision.
