# OCI Image Preflight Implementation Plan

> Execute inline in the isolated worktree; subagents are forbidden in this side conversation. The referenced executing-plans skill is unavailable locally.

**Goal:** Report OCI cache misses and pinned image-ID mismatches without pulling images.

**Architecture:** Add an optional `driver.ArtifactPreflight` interface, implemented by the Docker provider. Runtime selects the configured artifact driver and annotates its checks; API keeps its existing aggregation.

**Tech Stack:** Go, Docker SDK, HCL, testify.

## Global Constraints

- Work only in `/tmp/sysbox-oci-preflight` on `fix/oci-image-preflight`.
- No registry access, pull/load, release, consumer changes, or unrelated apply fixes.
- Follow the approved spec in `docs/superpowers/specs/2026-09-30-oci-image-preflight-design.md`.
- Tests use a fake Docker HTTP transport, never a real daemon.

## Task 1: Regression tests (red)

- [x] Add provider tests in `pkg/provider/docker/image_preflight_test.go` asserting the optional method exists and exercising matching, normalized, mismatched and unpinned IDs, cache miss, permission/connection/timeout errors. Each request must be GET image inspect. Unsupported-kind and resolved-source coverage added during regression.
- [x] Add runtime tests in `pkg/runtime/resource_image_preflight_test.go` through parsed HCL and a fake artifact driver. Assert driver selection, source and SHA forwarding, five-second deadline, resource naming, absence of ResolveImage calls, unsupported capability warning, registry errors, and secret deferral.
- [x] Add API tests in `pkg/api/preflight_oci_test.go` exercising the GET handler with a controlled artifact driver and no real node driver. Assert HTTP response checks and overall success/failure.
- [x] Run `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/tmp/sysbox-oci-go-build go test -mod=readonly ./pkg/provider/docker ./pkg/runtime ./pkg/api -run 'Test.*Preflight.*OCI|TestOCIImagePreflight' -count=1`. Observed assertion failures for missing OCI checks, not compilation errors.

## Task 2: Minimal implementation (green)

- [x] In `pkg/driver/capability.go`, add the optional interface from the approved spec; do not expand `Artifact`.
- [x] In `pkg/provider/docker/image_preflight.go`, inspect via the existing client, classify only NotFound as warning, normalize SHA with `normalizeDigest`, and return actionable checks without invoking ResolveImage.
- [x] In `pkg/runtime/resource_image.go`, retain non-OCI behavior and dispatch OCI checks with a five-second context. Handle registry errors, unsupported inspection, and execution-only secrets explicitly.
- [x] Repeat the red-test command and verify all cases pass.

## Task 3: Review and regression

- [x] Run `gofmt` on changed Go files and `git diff --check`.
- [x] Attempt full tests for `./pkg/driver ./pkg/provider/docker ./pkg/runtime ./pkg/api` with offline module settings. Driver/runtime pass; Docker/API are blocked by existing local-listener tests under this sandbox (details in spec).
- [x] Run all Preflight tests five times and with `-race`; run complete driver/runtime race tests; compile all packages using `go test ./... -run '^$'`. All pass.
- [x] Review the actual diff against the spec, especially no mutations, existing digest semantics, no secret leakage, and unchanged public HCL/JSON.
- [x] Record results and limitations, update spec progress; include only intended files in the local fix commit. No merge or push.

## Follow-up outside this sandbox

Run `go test -mod=readonly ./pkg/driver ./pkg/provider/docker ./pkg/runtime ./pkg/api -count=1` where local listeners are permitted. Real-daemon verification, release and downstream version bumps remain separate actions.
