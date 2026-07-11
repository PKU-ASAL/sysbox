# Core Identity And Plan Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Sysbox's string-concatenated resource identity and dual plan representation with canonical structured addresses, strict diagnostics, honest actions, and immutable stored-plan validation.

**Architecture:** A new leaf `pkg/address` package owns parsing, rendering, comparison, and JSON encoding. Graph, state, control-plane DTOs, runtime plans, configuration expansion, CLI, and API use that value directly. Runtime executes one ordered action list; stored plans bind configuration, state, driver, schema, artifact, and variable fingerprints before mutation.

**Tech Stack:** Go 1.22+, HashiCorp HCL v2, `zclconf/go-cty`, Cobra, standard `encoding/json`, existing Go test/testify stack.

## Global Constraints

- This is a deliberate breaking architecture release; do not add legacy address, state, or API compatibility adapters.
- Every change follows red-green-refactor and ends with focused plus repository-wide tests.
- `pkg/address` is a leaf package and imports only the Go standard library.
- Canonical addresses are `type.name`, `type.name[0]`, `type.name["key"]`, and `module.name[instance].type.name[key]`.
- Apply executes exactly the immutable actions displayed by plan; Update is not exposed until a handler performs an in-place update.
- Existing incompatible state is rejected without mutation.
- Do not implement Terraform plugin protocols, remote modules, state migration, or Windows support in this batch.

---

## File Structure

```text
pkg/address/
  address.go          immutable address and instance-key types
  parse.go            canonical parser with positioned errors
  json.go             stable text/JSON encoding
  address_test.go     parse/render/ordering/round-trip tests

pkg/diag/
  diagnostic.go       severity, summary, detail, source subject
  diagnostics.go      aggregation and error conversion
  diagnostics_test.go deterministic formatting tests

pkg/graph/
  graph.go            address-keyed nodes and lookup
  graph_test.go       address identity, dependency, cycle tests

pkg/config/
  diagnostic.go       HCL diagnostics conversion
  eval.go             strict locals/modules evaluation
  workspace_expand.go count/for-each/module address expansion
  workspace_expand_test.go canonical expansion tests

pkg/state/
  state.go            address-keyed resource identity, schema v3
  state_test.go       current schema round trip and rejection tests

pkg/controlplane/
  plan.go             address-based immutable action contract
  plan_test.go        JSON and action validation tests

pkg/runtime/
  plan.go             one action-list plan model
  plan_test.go        exact diff action tests
  apply.go            action-list executor
  destroy.go          delete action executor
  plan_fingerprint.go stored-plan fingerprints
  plan_fingerprint_test.go stale-plan rejection tests
  workspace.go        strict load pipeline
  workspace_test.go   diagnostics and canonical graph tests

pkg/api/
  plan_service.go      persist fingerprints with plans
  plan_service_test.go reject stale plan before dispatch

cmd/sysbox/commands/
  address.go           shared CLI address parsing
  state_cmd.go         structured state lookup
  import_cmd.go        structured import target
  plan_cmd.go          canonical display
```

### Task 1: Canonical Resource Address Value

**Files:**
- Create: `pkg/address/address.go`
- Create: `pkg/address/parse.go`
- Create: `pkg/address/json.go`
- Create: `pkg/address/address_test.go`

**Interfaces:**
- Produces: `address.Address`, `address.ModuleInstance`, `address.InstanceKey`, `address.Parse(string) (Address, error)`, `Address.String() string`, `Address.Less(Address) bool`.

- [ ] **Step 1: Write failing canonical round-trip tests**

```go
func TestAddressCanonicalRoundTrip(t *testing.T) {
    cases := []string{
        `sysbox_node.web`,
        `sysbox_node.web[0]`,
        `sysbox_node.web["front-end"]`,
        `module.network.sysbox_network.dmz`,
        `module.segment["red"].sysbox_node.target[1]`,
    }
    for _, input := range cases {
        got, err := Parse(input)
        require.NoError(t, err)
        require.Equal(t, input, got.String())
    }
}

func TestAddressRejectsFlattenedAndMalformedForms(t *testing.T) {
    for _, input := range []string{``, `sysbox_node`, `module_.node_x`, `sysbox_node.web[]`, `sysbox_node.web[key]`} {
        _, err := Parse(input)
        require.Error(t, err, input)
    }
}
```

- [ ] **Step 2: Run the address tests and verify failure**

Run: `go test ./pkg/address`

Expected: FAIL because `pkg/address` and `Parse` do not exist.

- [ ] **Step 3: Implement immutable address and key types**

```go
type KeyKind uint8

const (
    NoKey KeyKind = iota
    IntKey
    StringKey
)

type InstanceKey struct {
    kind KeyKind
    num  int
    str  string
}

type ModuleInstance struct {
    Name string
    Key  InstanceKey
}

type Address struct {
    ModulePath []ModuleInstance
    Type       string
    Name       string
    Key        InstanceKey
}

func Resource(typ, name string) Address
func IntInstance(typ, name string, index int) Address
func StringInstance(typ, name, key string) Address
func IntKeyValue(index int) InstanceKey
func StringKeyValue(key string) InstanceKey
func (a Address) WithModule(module ModuleInstance) Address
func (a Address) IsZero() bool
func (a Address) Equal(other Address) bool
func (a Address) Less(other Address) bool
func (a Address) String() string
```

Validate identifiers with HCL identifier rules, encode string keys with `strconv.Quote`, deep-copy module paths in constructors, and never expose mutable slice aliases.

- [ ] **Step 4: Implement strict parser and text/JSON encoding**

`Parse` must consume the complete input and return errors containing the byte offset and expected token. Implement `encoding.TextMarshaler`, `encoding.TextUnmarshaler`, `json.Marshaler`, and `json.Unmarshaler` using the canonical string.

- [ ] **Step 5: Add JSON, equality, copy-safety, and ordering tests**

```go
func TestAddressJSONUsesCanonicalString(t *testing.T) {
    input := StringInstance("sysbox_node", "web", "front-end")
    raw, err := json.Marshal(input)
    require.NoError(t, err)
    require.JSONEq(t, `"sysbox_node.web[\"front-end\"]"`, string(raw))
    var output Address
    require.NoError(t, json.Unmarshal(raw, &output))
    require.True(t, input.Equal(output))
}
```

- [ ] **Step 6: Run focused and repository tests**

Run: `go test ./pkg/address && go test ./...`

Expected: address tests PASS; repository tests PASS because no existing consumer changed.

- [ ] **Step 7: Commit**

```bash
git add pkg/address
git commit -m "feat(core): add canonical resource addresses"
```

### Task 2: Address-Keyed Dependency Graph

**Files:**
- Modify: `pkg/graph/graph.go`
- Modify: `pkg/graph/graph_test.go`
- Modify: `pkg/graph/walker.go`
- Modify: all compile-error call sites returned by `go test ./...`

**Interfaces:**
- Consumes: `address.Address` from Task 1.
- Produces: `graph.Node{Address address.Address}`, `Graph.AddNode(address.Address, []address.Address)`, `Graph.Get(address.Address)`, `Graph.SetData(address.Address, any)`.

- [ ] **Step 1: Replace graph tests with structured-address behavior**

```go
func TestGraphKeepsForEachAndModuleInstancesDistinct(t *testing.T) {
    g := New()
    root := address.StringInstance("sysbox_node", "web", "blue")
    child := address.Address{
        ModulePath: []address.ModuleInstance{{Name: "lab"}},
        Type: "sysbox_node", Name: "web", Key: address.StringKeyValue("blue"),
    }
    g.AddNode(root, nil)
    g.AddNode(child, nil)
    require.Len(t, g.All(), 2)
    require.NotNil(t, g.Get(root))
    require.NotNil(t, g.Get(child))
}
```

- [ ] **Step 2: Run graph tests and verify compile failure**

Run: `go test ./pkg/graph`

Expected: FAIL because graph still accepts type/name strings.

- [ ] **Step 3: Replace `NodeID` and `Ref` with `address.Address`**

Use `map[string]*Node` internally, keyed by `Address.String()`, because Address contains a slice and is not comparable. Reject duplicate addresses in `AddNode` with an error instead of overwriting. Sort `All()` by `Address.Less` for deterministic plans and diagnostics.

- [ ] **Step 4: Update graph validation and walkers**

Cycle and dangling-reference errors must render canonical addresses. Topological ordering must use canonical address ordering when multiple nodes are ready, making plan output deterministic.

- [ ] **Step 5: Mechanically migrate consumers without compatibility aliases**

Replace every `graph.NodeID{Type: ..., Name: ...}` with `address.Resource(...)` or an already available structured address. Replace `.ID` with `.Address`; delete `type Ref = NodeID`.

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/graph ./pkg/runtime ./pkg/api ./cmd/sysbox/commands && go test ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/graph pkg/runtime pkg/api cmd/sysbox/commands
git commit -m "refactor(core): key dependency graph by resource address"
```

### Task 3: Structured State Identity And Breaking Schema V3

**Files:**
- Modify: `pkg/state/state.go`
- Modify: `pkg/state/state_test.go`
- Modify: `pkg/state/manager.go`
- Modify: state consumers identified by `rg '\.(Type|Name)' pkg cmd -g '*.go'`

**Interfaces:**
- Consumes: `address.Address`.
- Produces: `state.Resource.Address`, `State.FindResource(address.Address)`, `State.RemoveResource(address.Address)`, schema version 3.

- [ ] **Step 1: Write failing state v3 tests**

```go
func TestStateV3RoundTripPreservesCanonicalAddress(t *testing.T) {
    want := New("run-1")
    want.AddResource(Resource{Address: address.StringInstance("sysbox_node", "web", "blue"), Driver: "docker", Attributes: map[string]any{"id": "c1"}})
    raw, err := want.Marshal()
    require.NoError(t, err)
    got, err := Unmarshal(raw)
    require.NoError(t, err)
    require.NotNil(t, got.FindResource(address.StringInstance("sysbox_node", "web", "blue")))
}

func TestStateRejectsV2WithoutMutation(t *testing.T) {
    _, err := Unmarshal([]byte(`{"version":2,"resources":[]}`))
    var incompatible *IncompatibleVersionError
    require.ErrorAs(t, err, &incompatible)
    require.Equal(t, 2, incompatible.Found)
    require.Equal(t, 3, incompatible.Expected)
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./pkg/state`

Expected: FAIL because Resource has Type/Name and schema version is 2.

- [ ] **Step 3: Replace state identity fields**

```go
const SchemaVersion = 3

type Resource struct {
    Address    address.Address `json:"address"`
    Driver     string          `json:"driver"`
    Attributes map[string]any  `json:"attributes"`
    Private    json.RawMessage `json:"private,omitempty"`
    CreatedAt  string          `json:"created_at,omitempty"`
    UpdatedAt  string          `json:"updated_at,omitempty"`
}
```

In Batch 1, existing instance contents move mechanically into `Attributes`; typed attribute enforcement belongs to Batch 2. Delete the v2 primary-IP migration and all Type/Name compatibility fields.

- [ ] **Step 4: Migrate state lookups and logs**

All lookups accept `address.Address`. External labels and logs use `Address.String()`. State JSON contains only `address`, never duplicated `type` and `name` identity.

- [ ] **Step 5: Run state and repository tests**

Run: `go test ./pkg/state ./pkg/runtime ./pkg/api ./cmd/sysbox/commands && go test ./...`

Expected: PASS with updated fixtures.

- [ ] **Step 6: Commit**

```bash
git add pkg/state pkg/runtime pkg/api cmd/sysbox/commands
git commit -m "refactor(state): adopt canonical resource identity"
```

### Task 4: Canonical HCL Instance Expansion

**Files:**
- Create: `pkg/config/diagnostic.go`
- Create: `pkg/runtime/workspace_expand.go`
- Create: `pkg/runtime/workspace_expand_test.go`
- Modify: `pkg/config/eval.go`
- Modify: `pkg/runtime/workspace.go`
- Modify: `pkg/config/parser_test.go`

**Interfaces:**
- Consumes: address constructors and address-keyed graph.
- Produces: canonical count, for-each, and module addresses; no flattened names.

- [ ] **Step 1: Add failing expansion tests**

Create an in-memory HCL fixture containing root count, root for-each, and module for-each resources. Assert graph addresses exactly equal:

```go
[]string{
    `sysbox_node.worker[0]`,
    `sysbox_node.worker[1]`,
    `sysbox_node.target["blue-team"]`,
    `module.segment.sysbox_network.dmz`,
}
```

Also assert `target["a_b"]` and a literal resource named `target_a_b` coexist.

- [ ] **Step 2: Run and verify failure**

Run: `go test ./pkg/runtime -run 'TestWorkspaceExpansion'`

Expected: FAIL because for-each and modules are flattened.

- [ ] **Step 3: Separate expansion from workspace loading**

Move count, for-each, and module traversal into `workspace_expand.go`. Pass a base `address.Address` to resource decoding; never rewrite the resource name. Module expansion appends `address.ModuleInstance` to `ModulePath`.

- [ ] **Step 4: Make ordering deterministic**

Sort map/object for-each keys before adding graph nodes. Preserve set ordering through canonical string sort. Reject unknown and sensitive instance keys during expansion.

- [ ] **Step 5: Delete flattened ID helpers and update references**

Remove `module_<name>_` and `<name>_<key>` generation. Reference extraction must produce structured keys. Update examples whose state or tests assert flattened names.

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/config ./pkg/runtime && go test ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/config pkg/runtime examples tests
git commit -m "refactor(config): expand canonical resource instances"
```

### Task 5: Strict Diagnostics Pipeline

**Files:**
- Create: `pkg/diag/diagnostic.go`
- Create: `pkg/diag/diagnostics.go`
- Create: `pkg/diag/diagnostics_test.go`
- Modify: `pkg/config/eval.go`
- Modify: `pkg/config/parser.go`
- Modify: `pkg/runtime/workspace.go`
- Modify: `cmd/sysbox/commands/validate_cmd.go`

**Interfaces:**
- Produces: `diag.Diagnostics`, `Diagnostics.HasErrors()`, `Diagnostics.Err()`, deterministic text and JSON forms.

- [ ] **Step 1: Write failing diagnostics tests**

Test that an invalid local, missing module variable, invalid output, duplicate resource address, dangling reference, and missing required environment value each produce one error containing file, line, column, summary, and detail.

- [ ] **Step 2: Run and verify current silent behavior fails assertions**

Run: `go test ./pkg/config ./pkg/runtime -run 'Diagnostic|InvalidLocal|MissingModule'`

Expected: FAIL because errors are skipped or lose source ranges.

- [ ] **Step 3: Implement diagnostic aggregation**

```go
type Severity string
const (
    Error Severity = "error"
    Warning Severity = "warning"
)

type Diagnostic struct {
    Severity Severity
    Summary  string
    Detail   string
    Subject  *SourceRange
    Address  *address.Address
}
```

Define `SourceRange{Filename string, Start, End SourcePos}` and
`SourcePos{Line, Column, Byte int}` in `pkg/diag`. Keep `pkg/diag` independent
of HCL and convert HCL ranges in `pkg/config/diagnostic.go`.

- [ ] **Step 4: Remove every silent diagnostic branch**

Replace all `continue`, empty return, or best-effort behavior following `diags.HasErrors()` in locals, modules, count, for-each, output, and provider config evaluation with aggregated errors. `env("NAME")` remains optional only where the consuming attribute is optional; required empty values fail schema validation.

- [ ] **Step 5: Make validate print stable diagnostics**

Sort by filename, start byte, severity, and summary. CLI returns non-zero on any error. API uses the same JSON DTO and status 422.

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/diag ./pkg/config ./pkg/runtime ./cmd/sysbox/commands ./pkg/api && go test ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/diag pkg/config pkg/runtime pkg/api cmd/sysbox/commands
git commit -m "feat(config): enforce strict topology diagnostics"
```

### Task 6: One Immutable Plan Action Model

**Files:**
- Modify: `pkg/controlplane/plan.go`
- Create: `pkg/controlplane/plan_test.go`
- Rewrite: `pkg/runtime/plan.go`
- Modify: `pkg/runtime/apply.go`
- Modify: `pkg/runtime/destroy.go`
- Modify: `pkg/runtime/refresh.go`
- Modify: `pkg/runtime/plan_test.go`

**Interfaces:**
- Produces: `controlplane.PlannedChange` and `runtime.Plan{Actions []PlannedChange}` as the sole plan representation.

- [ ] **Step 1: Write failing action-contract tests**

```go
func TestPlanRejectsUnsupportedUpdate(t *testing.T) {
    plan := Plan{Actions: []controlplane.PlannedChange{{Address: address.Resource("sysbox_node", "web"), Action: controlplane.PlanActionUpdate}}}
    require.ErrorContains(t, plan.Validate(), "update is not supported")
}

func TestApplyExecutesActionsInPlanOrder(t *testing.T) {
    // Use recording handlers and assert Create A, Create B, Delete C exactly;
    // no Add/Change/Destroy side indexes exist or are recomputed.
}
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./pkg/controlplane ./pkg/runtime -run 'Plan|ApplyExecutes'`

Expected: FAIL because Plan has dual slices and Update exists.

- [ ] **Step 3: Replace wire action contract**

```go
type PlanActionType string
const (
    PlanActionNoOp PlanActionType = "no-op"
    PlanActionCreate PlanActionType = "create"
    PlanActionRead PlanActionType = "read"
    PlanActionReplace PlanActionType = "replace"
    PlanActionDelete PlanActionType = "delete"
    PlanActionUnknown PlanActionType = "unknown"
)

type PlannedChange struct {
    Address           address.Address       `json:"address"`
    Action            PlanActionType        `json:"action"`
    Reason            string                `json:"reason,omitempty"`
    DependencyReason  string                `json:"dependency_reason,omitempty"`
    Changes           map[string]FieldChange `json:"changes,omitempty"`
}
```

Delete duplicated Resource/Type/Name fields, Update, Skip, NodeID conversion, and all legacy indexes from runtime Plan.

- [ ] **Step 4: Generate actions in deterministic dependency order**

Create/Read/Replace follow topological order. Delete follows reverse topological order. Replacement is represented as one action whose executor performs explicit delete then create substeps. No-op actions are retained for display but skipped by executor.

- [ ] **Step 5: Make `prevent_destroy` a plan error**

Any Delete or Replace against a protected resource returns a diagnostic and no executable plan. Remove Protected and Skip semantics.

- [ ] **Step 6: Rewrite apply/destroy/refresh against actions only**

Delete `ensureActions`, `PlanFromActions`, `addDesiredAction`, `actionsByType`, `actionFor`, `reasonFor`, and side slices. Refresh returns a new immutable plan instead of mutating action indexes in place.

- [ ] **Step 7: Run tests**

Run: `go test ./pkg/controlplane ./pkg/runtime ./pkg/api ./cmd/sysbox/commands && go test ./...`

Expected: PASS; no `PlanActionUpdate`, `PlanActionSkip`, `.Add`, `.Change`, `.Destroy`, or `.Protected` references remain.

- [ ] **Step 8: Commit**

```bash
git add pkg/controlplane pkg/runtime pkg/api cmd/sysbox/commands
git commit -m "refactor(plan): execute one immutable action model"
```

### Task 7: Stored-Plan Fingerprints

**Files:**
- Create: `pkg/runtime/plan_fingerprint.go`
- Create: `pkg/runtime/plan_fingerprint_test.go`
- Modify: `pkg/controlplane/model.go`
- Modify: `pkg/api/plan_service.go`
- Modify: `pkg/api/plan_service_test.go`
- Modify: `cmd/sysbox/commands/plan_cmd.go`
- Modify: `cmd/sysbox/commands/apply_cmd.go`

**Interfaces:**
- Produces: `PlanFingerprint`, `BuildPlanFingerprint(PlanInputs)`, `ValidatePlanFingerprint(expected, actual) error`.

- [ ] **Step 1: Write failing fingerprint tests**

Use a table test changing one field at a time: configuration bytes, state lineage, state serial, schema version map, driver version map, artifact digest map, and non-secret variable digest. Every change must reject apply before a recording handler observes a call.

- [ ] **Step 2: Run and verify failure**

Run: `go test ./pkg/runtime ./pkg/api -run 'Fingerprint|StalePlan'`

Expected: FAIL because stored plans bind only partial revision/serial data.

- [ ] **Step 3: Implement deterministic fingerprint types**

```go
type PlanFingerprint struct {
    ConfigSHA256     string            `json:"config_sha256"`
    StateLineage     string            `json:"state_lineage"`
    StateSerial      int64             `json:"state_serial"`
    ResourceSchemas map[string]int     `json:"resource_schemas"`
    Drivers         map[string]string  `json:"drivers"`
    Artifacts       map[string]string  `json:"artifacts"`
    VariablesSHA256 string            `json:"variables_sha256"`
}
```

Canonicalize HCL input as the exact workspace revision bytes, sort all map keys before hashing, and hash secret references rather than resolved secret values.

- [ ] **Step 4: Persist and validate fingerprints**

Both CLI saved plans and API plan records store the fingerprint. Apply loads current inputs, compares all fields, returns a field-specific stale-plan error, and performs no external call on mismatch.

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/runtime ./pkg/api ./cmd/sysbox/commands && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/runtime pkg/controlplane pkg/api cmd/sysbox/commands
git commit -m "feat(plan): bind stored plans to reproducible inputs"
```

### Task 8: Batch 1 Integration, Documentation, And Removal Audit

**Files:**
- Modify: `README.md`
- Modify: `docs/api.md`
- Modify: relevant files under `examples/`
- Modify: relevant files under `tests/testdata/`
- Create: `docs/architecture/resource-addresses.md`
- Create: `docs/architecture/stored-plan-contract.md`

**Interfaces:**
- Verifies all Batch 1 contracts; produces user-facing breaking-change documentation.

- [ ] **Step 1: Add black-box CLI fixture**

Create a fixture with module, count, and for-each instances. Validate, plan, apply with a fake resource handler, state list, state show by canonical address, save plan, mutate the fixture, and assert saved-plan apply fails before mutation.

- [ ] **Step 2: Run the black-box test and verify any remaining failures**

Run: `go test ./cmd/sysbox/commands ./tests/...`

Expected before fixes: FAIL on every stale string-address or dual-plan consumer found by the fixture.

- [ ] **Step 3: Remove legacy symbols and behavior**

Run these searches and make each return no matches outside historical documentation:

```bash
rg 'NodeID|PlanActionUpdate|PlanActionSkip|module_.*_|PlanFromActions|ensureActions|\.Protected|\.Change|\.Destroy|\.Add' pkg cmd
rg 'json:"type"|json:"name"' pkg/state pkg/controlplane
```

- [ ] **Step 4: Document canonical addresses and stored-plan invalidation**

Document exact CLI examples for count, string-key for-each, module addresses, state lookup, plan JSON, incompatible state v2 rejection, and the required old-binary destroy/new-binary apply upgrade procedure.

- [ ] **Step 5: Run formatting, static checks, and all tests**

Run:

```bash
gofmt -w pkg cmd
go vet ./...
go test ./...
```

Expected: all commands exit 0.

- [ ] **Step 6: Run focused race tests**

Run: `go test -race ./pkg/graph ./pkg/state ./pkg/runtime ./pkg/api`

Expected: PASS with no data race.

- [ ] **Step 7: Commit**

```bash
git add README.md docs examples tests pkg cmd
git commit -m "docs: publish breaking core identity contract"
```

- [ ] **Step 8: Request code review**

Review against Sections 4 and 11 of the design specification. Block Batch 2 until all correctness findings are resolved and the full suite remains green.
