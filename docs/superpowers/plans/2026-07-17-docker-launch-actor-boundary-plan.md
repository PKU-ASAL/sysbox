# Docker Launch Overrides and Actor Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove ACP actor orchestration from Sysbox core and add deterministic Docker-only OCI entrypoint/command overrides.

**Architecture:** `sysbox_node` remains substrate-neutral. Docker decodes presence-aware launch overrides in its provider block, computes effective image entry metadata during create/reset, and persists it in provider state. Actor roles and ACP metadata leave the infrastructure graph entirely.

**Tech Stack:** Go, HCL v2, Docker Engine API, testify, Bash acceptance tests

## Global Constraints

- Do not preserve `sysbox_actor` HCL, state, runtime, or API compatibility.
- Do not add container launch fields to the common `sysbox_node` schema.
- Preserve omitted, non-empty, and explicitly empty array semantics.
- Execute effective launch values as direct argv without an implicit shell.
- Launch override changes replace the node.
- State/reset remain bound to the verified immutable image identity.

---

### Task 1: Remove Actor Resource Semantics

**Files:**
- Modify: `pkg/config/parser_test.go`
- Modify: `pkg/config/schema.go`
- Modify: `pkg/runtime/schema.go`
- Modify: `pkg/runtime/desired.go`
- Delete: `pkg/runtime/resource_actor.go`
- Modify: actor references discovered under `pkg/runtime`, `pkg/controlplane`, and `pkg/api`

**Interfaces:**
- Consumes: existing strict unknown-resource diagnostics.
- Produces: no registered `sysbox_actor` resource or actor-specific state fields.

- [ ] **Step 1: Write failing removal tests**

Add a parser/loader test using:

```hcl
resource "sysbox_actor" "red" {
  position = "internal"
  node     = sysbox_node.attack.id
  command  = ["opencode", "serve"]
}
```

Assert validation fails and no active schema registry contains
`sysbox_actor`. Add repository assertions that production Go code contains no
`ActorConfig`, `entry_points`, or `acp_url` fields.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./pkg/config ./pkg/runtime ./pkg/api`

Expected: FAIL because actor remains registered and accepted.

- [ ] **Step 3: Delete actor production paths**

Remove the config type, resource schema/desired encoding, graph dispatch,
runtime create/delete/recovery code, actor-only control-plane values, and
actor-specific API behavior. Keep generic node sessions and node operations.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `go test ./pkg/config ./pkg/runtime ./pkg/api`

Expected: PASS.

### Task 2: Decode Presence-Aware Docker Launch Overrides

**Files:**
- Create: `pkg/provider/docker/config_test.go`
- Modify: `pkg/provider/docker/config.go`
- Modify: `pkg/provider/docker/docker.go`

**Interfaces:**
- Produces: `OptionalArgv{Set bool, Value []string}` and Docker `Config` fields
  `Entrypoint OptionalArgv`, `Command OptionalArgv`.

- [ ] **Step 1: Write failing decoder tests**

Decode provider bodies for omitted attributes, non-empty arrays, explicit
empty arrays, and invalid string values. Assert omission differs from `[]`.

- [ ] **Step 2: Run test and verify RED**

Run: `go test ./pkg/provider/docker -run 'DecodeProviderConfig.*Launch' -count=1`

Expected: FAIL because the fields and presence model do not exist.

- [ ] **Step 3: Implement strict decoding**

Use an HCL body schema to retain attribute presence, evaluate each expression
to a tuple/list of strings, and reject unknown attributes or non-array values.
Continue decoding existing boolean/string/list provider attributes.

- [ ] **Step 4: Run test and verify GREEN**

Run: `go test ./pkg/provider/docker -count=1`

Expected: PASS.

### Task 3: Compute and Persist Effective Docker Launch

**Files:**
- Modify: `pkg/provider/docker/node.go`
- Modify: `pkg/provider/docker/reset.go`
- Modify: `pkg/provider/docker/reset_test.go`
- Add or modify: `pkg/provider/docker/node_test.go`

**Interfaces:**
- Consumes: image `Config.Entrypoint`, image `Config.Cmd`, and decoded Docker
  overrides.
- Produces: `HandleState.ImageEntrypoint` and `HandleState.ImageCmd` containing
  the effective launch arrays used by `ExecImageEntry` and reset.

- [ ] **Step 1: Write failing effective-launch tests**

Cover inherit/inherit, inherit/override, override/inherit, override/override,
explicit clear, empty effective argv, and metacharacter-preserving direct argv.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./pkg/provider/docker -run 'Launch|ImageEntry|Reset' -count=1`

Expected: FAIL because handle state always records image defaults.

- [ ] **Step 3: Implement effective launch computation**

Apply presence-aware overrides immediately after immutable image inspection.
Persist effective arrays in handle/reset state. Keep idle-container creation and
start effective argv only after provisioning.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./pkg/provider/docker -count=1`

Expected: PASS.

### Task 4: Prove Planning and Reset Behavior

**Files:**
- Modify: `pkg/runtime/resource_node_test.go`
- Modify: `pkg/runtime/reset_test.go`
- Modify: `pkg/runtime/desired.go` only if typed provider desired values need normalization

**Interfaces:**
- Consumes: typed Docker provider config in node desired values.
- Produces: provider launch changes as node replacement and reset requests that
  carry the declared provider config.

- [ ] **Step 1: Write failing runtime tests**

Assert a command or entrypoint change alters the node desired value and plans a
replacement. Assert reset passes the typed provider config to the Docker reset
driver and executes image entry after provisioning.

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./pkg/runtime -run 'ProviderConfig|Reset|Launch' -count=1`

Expected: FAIL at any missing lifecycle propagation.

- [ ] **Step 3: Implement minimum propagation fixes**

Normalize optional launch values deterministically in desired payloads and
preserve provider config through reset without adding common node fields.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./pkg/runtime -count=1`

Expected: PASS.

### Task 5: Remove Public Actor Examples and Document Launch Overrides

**Files:**
- Modify: `README.md`
- Modify: `docs/overview.md`
- Modify: `docs/architecture/secrets.md`
- Modify: `examples/three-nodes/field.sysbox.hcl`
- Modify: `examples/mixed/field.sysbox.hcl`
- Delete or rewrite: ACP-only example helpers under `examples/three-nodes` and `examples/mixed-capture`
- Modify: release/change documentation as required

**Interfaces:**
- Produces: active documentation with no actor/ACP resource claims and a Docker
  provider launch example.

- [ ] **Step 1: Add a failing repository audit**

Assert active product code/docs/examples contain no `sysbox_actor`,
`ActorConfig`, `entry_points`, or `acp_url`, excluding historical specs/plans.

- [ ] **Step 2: Run audit and verify RED**

Run: the repository audit test or equivalent `rg` command.

Expected: FAIL with current actor references.

- [ ] **Step 3: Rewrite active examples and docs**

Represent attackers as ordinary nodes, remove ACP runner helpers from product
examples, document Docker-only launch semantics and the breaking workspace
recreation requirement.

- [ ] **Step 4: Run audit and verify GREEN**

Run: `go test ./...` plus the repository audit.

Expected: PASS with no active actor surface.

### Task 6: End-to-End Verification

**Files:**
- Create or modify: focused Docker acceptance under `tests/e2e`
- Modify: `Makefile` only when required to expose the acceptance target

**Interfaces:**
- Produces: evidence for validate, plan, apply, reset, no-op plan, and destroy.

- [ ] **Step 1: Add acceptance fixture and assertions**

Use a locally built image with known defaults. Verify inherited launch,
command override, explicit clear, reset preservation, no-op plan, and cleanup.

- [ ] **Step 2: Run host-safe verification**

Run: `go test ./...`, `go vet ./...`, and `git diff --check`.

Expected: PASS.

- [ ] **Step 3: Run Docker acceptance**

Run the repository's privileged Docker acceptance target.

Expected: PASS; no managed containers or networks remain afterward.

- [ ] **Step 4: Final audit**

Review the implementation against the approved spec, fix every critical or
important issue, rerun focused and full verification, and record any external
environment limitation explicitly.
