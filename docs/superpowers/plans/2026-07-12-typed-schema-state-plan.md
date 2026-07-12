# Typed Schema And State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ad hoc state maps and hash-led diffing with typed schemas, typed state v4, explicit observations, backend safety capabilities, and secret references that never persist plaintext.

**Architecture:** `pkg/value` owns deterministic typed dynamic values and attribute paths without importing runtime or state. Runtime schemas validate desired values and drive semantic diffs. State v4 stores typed public values and opaque versioned driver-private bytes; refresh and mutation services enforce explicit observation and backend capability contracts.

**Tech Stack:** Go 1.26, HCL v2/cty, standard JSON, existing state backends and testify tests.

## Global Constraints

- This is deliberately breaking; state v3 is rejected and no migration adapter is added.
- Public attributes and driver-private data never share storage.
- Desired hashes remain audit-only and never decide semantic actions.
- Unknown observations never trigger destructive repair.
- Mutation requires lock and CAS unless an explicit unsafe override is recorded.
- Resolved secret plaintext never enters plans, state, checkpoints, API payloads, or logs.
- Every task follows RED, verified RED, GREEN, repository tests, audit, and an atomic commit.

---

### Task 1: Typed Dynamic Values And Attribute Paths

**Files:**
- Create: `pkg/value/type.go`, `pkg/value/value.go`, `pkg/value/path.go`, `pkg/value/json.go`
- Create: `pkg/value/value_test.go`, `pkg/value/path_test.go`

**Interfaces:**
- Produces: `value.Type`, `value.Value`, `value.Path`, `value.FromGo(any)`, `Value.GoValue()`, deterministic JSON and `value.Diff(before, after)`.

- [ ] Write failing tests for null/bool/number/string/list/map/object round trips, numeric preservation, nested paths such as `interfaces[0].ip`, deterministic map JSON, and deep-copy ownership.
- [ ] Run `go test ./pkg/value` and verify missing package/type failures.
- [ ] Implement immutable tagged values; reject unsupported Go values and heterogeneous collections where schema requires one element type.
- [ ] Implement structural path parsing/rendering and deterministic recursive diff.
- [ ] Run `go test ./pkg/value && go test ./...`.
- [ ] Commit with `feat(value): add typed dynamic values and paths`.

### Task 2: Declarative Resource Schemas And Typed Diff

**Files:**
- Rewrite: `pkg/runtime/schema.go`
- Modify: `pkg/runtime/desired.go`, resource provider schema declarations
- Create: `pkg/runtime/schema_test.go`, `pkg/runtime/typed_diff_test.go`

**Interfaces:**
- Consumes: `value.Type`, `value.Value`, `value.Path`.
- Produces: `AttributeSchema{Type, Required, Optional, Computed, Sensitive, Behavior, Default, Nested, Persist}`, `ResourceSchema.Validate`, `ResourceSchema.Diff`.

- [ ] Write failing tests for required/optional conflicts, wrong types, defaults, nested validation, sensitive redaction, immutable replacement, ignored typed paths, and computed-only changes.
- [ ] Verify RED with `go test ./pkg/runtime -run 'Schema|TypedDiff'`.
- [ ] Implement schema validation and replace `map[string]FieldChange` with ordered path changes.
- [ ] Remove desired-hash action decisions; retain the hash only in state audit metadata.
- [ ] Declare schemas for every registered resource and make unknown attributes validation errors.
- [ ] Run focused and full tests, then commit `refactor(schema): drive planning with typed resource schemas`.

### Task 3: Breaking Typed State V4 And Driver-Private Codec

**Files:**
- Rewrite: `pkg/state/state.go`
- Create: `pkg/state/private.go`, `pkg/state/testdata/state-v4.json`, `pkg/state/state_v4_test.go`
- Modify: all state producers/consumers returned by compile errors.

**Interfaces:**
- Produces: `state.Resource{Address, ResourceType, Driver, SchemaVersion, ExternalID, Attributes value.Value, Private json.RawMessage, Dependencies, Status, CreatedAt, UpdatedAt}` and `PrivateEnvelope{Version, Payload}`.

- [ ] Write failing golden round-trip, deterministic marshal, deep-copy, corrupt private envelope, and v3 rejection tests.
- [ ] Verify RED with `go test ./pkg/state -run 'V4|Private|Incompatible'`.
- [ ] Set `state.SchemaVersion = 4`; remove stringly accessors and all implicit migration code.
- [ ] Move `provider_extra`, runtime IDs, PIDs, namespaces, bridge names, and provider handles into versioned `Private`; retain only schema-approved public/computed attributes.
- [ ] Migrate runtime/API/CLI/checkpoint consumers to typed accessors and explicit private codecs.
- [ ] Run full tests and commit `refactor(state): introduce typed state v4`.

### Task 4: Explicit Read Observations And CAS Refresh

**Files:**
- Rewrite: `pkg/runtime/resource_provider.go`, `pkg/runtime/refresh.go`
- Modify: every resource `Read`, `pkg/api/health.go`, apply service state persistence
- Create: `pkg/runtime/refresh_state_test.go`

**Interfaces:**
- Produces: `ObservationStatus` values `present/absent/drifted/degraded/unknown` and `ReadResult{Status, State, Diagnostics}`.

- [ ] Write failing tests proving absent plans replacement, unknown blocks apply, degraded is reported without deletion, and computed fields persist using expected state serial.
- [ ] Verify RED with `go test ./pkg/runtime -run 'Refresh|Observation'`.
- [ ] Implement explicit status returns; eliminate error-as-healthy behavior and dependency-wide replacement cascades.
- [ ] Persist refreshed computed state via manager CAS before any destructive action.
- [ ] Run focused/full tests and commit `refactor(refresh): persist explicit resource observations`.

### Task 5: Backend Capability Enforcement

**Files:**
- Modify: `pkg/state/backend.go`, backend implementations, `pkg/state/manager.go`
- Modify: CLI/API/agent mutation entry points
- Create: `pkg/state/capabilities_test.go`, `pkg/api/backend_safety_test.go`

**Interfaces:**
- Produces: `BackendCapabilities{Locking, CAS, Snapshot, Delete, Lease, ForceUnlock, SafeMutation}` and `Manager.RequireMutationSafety(allowUnsafe bool)`.

- [ ] Write failing table tests for local/SQLite/Postgres safe capability sets and HTTP/S3 unsafe sets.
- [ ] Write failing tests proving apply/destroy/import/recovery reject unsafe backends before provider calls, while explicit override marks run/checkpoint unsafe.
- [ ] Implement capability advertisement and enforce it in the shared application mutation path.
- [ ] Run full tests and commit `feat(state): enforce backend mutation capabilities`.

### Task 6: Secret References And Artifact Canary Audit

**Files:**
- Create: `pkg/secret/reference.go`, `pkg/secret/resolver.go`, tests
- Modify: configuration fields for node/actor env, connections, provisioners, SSH keys, provider config
- Modify: plan/state/checkpoint/API/log serializers
- Create: `tests/secret_canary_test.go`

**Interfaces:**
- Produces: `secret.Reference{Source, Name}`, `Resolver.Resolve(context.Context, Reference)`, execution-scoped resolved values.

- [ ] Write failing tests that configure one canary secret across every sensitive field and scan plan, state, checkpoint, API JSON, and captured logs for plaintext.
- [ ] Verify RED with `go test ./pkg/secret ./tests -run Secret`.
- [ ] Parse secret references without resolution; hash references, not values, in fingerprints.
- [ ] Resolve immediately before driver calls, zero/drop resolved collections afterward, and redact diagnostics.
- [ ] Run canary/full/race tests and commit `feat(secret): keep resolved values outside durable artifacts`.

### Task 7: Batch 2 Integration And Documentation

**Files:**
- Modify: `README.md`, `docs/api.md`
- Create: `docs/architecture/typed-state.md`, `docs/architecture/backend-safety.md`, `docs/architecture/secrets.md`

**Interfaces:** Verifies all Batch 2 contracts.

- [ ] Audit for `Attributes map[string]any`, stringly state accessors, `provider_extra` in public attributes, desired-hash planning, probe-error-as-healthy, and plaintext sensitive serialization; allow no production matches.
- [ ] Document state v4 rejection, schema behavior, observation statuses, unsafe override, and secret reference syntax.
- [ ] Run `go test ./...`, `go vet ./...`, `go test -race ./pkg/value ./pkg/state ./pkg/runtime ./pkg/api`, and `git diff --check`.
- [ ] Review Sections 5 and 11 of the design specification and resolve every correctness finding.
- [ ] Commit `docs: publish typed state and safety contracts`.
