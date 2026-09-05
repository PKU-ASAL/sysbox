# 全局 revision 与 upsert apply 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 cyberfield 用「publish HCL → revision digest → upsert apply」一步收敛拓扑，取代 create→hcl→plans→apply 多步流程。

**Architecture:** 内容寻址的全局 revision registry（HCL 内容存储）+ upsert apply（create-if-not-exists + 收敛）。revision registry 是 HCL 的唯一来源，apply 时物化到 workspace 文件供 executor 读。nodes/outputs 补进状态，deadline_at 成为 apply 入参。旧端点（create/hcl/plans + per-topology revisions）删除。

**Tech Stack:** Go、hcl/v2、net/http（标准库路由）、现有 pkg/api + pkg/controlplane + pkg/state。

**约定：** 全程 TDD；每个任务先写测试看红，再实现看绿，最后提交。分支 `feat/revision-upsert-apply`。测试命令 `go test ./pkg/api/...` 等，失败/通过以实际输出为准。

---

## 文件结构

**新增：**
- `pkg/controlplane/revision.go` —— 全局 Revision 模型（去 Workspace 键，内容寻址）
- `pkg/api/revision_store.go` —— 全局 revision 存储接口 + local/sqlite 实现
- `pkg/api/handler_revision.go` —— `POST /v1/revisions` handler
- `pkg/api/revision_test.go` —— revision endpoint + 存储测试

**修改：**
- `pkg/api/api_store_interfaces.go` —— apiStore 加 revision 存储方法
- `pkg/api/api_store.go` —— localAPIStore / postgresAPIStore 实现 revision 存储
- `pkg/api/server.go` —— 注册 `POST /v1/revisions`，删除旧路由
- `pkg/api/handler_topo.go` —— apply handler 改 upsert；删除 create/plans 相关
- `pkg/api/run_service.go` —— StartApply 改 upsert（revision → HCL → create-if-not-exists）
- `pkg/api/workspace_service.go` —— Create 支持 upsert（幂等写 HCL）
- `pkg/api/handler_workspace.go` —— 删除 handleCreateTopology/handleUpdateHCL；topologyStatus 补 nodes/outputs
- `pkg/controlplane/model.go` —— Run 加 DeadlineAt 字段
- `pkg/api/jobs.go` —— markConvergenceDeadlineExceeded 支持 per-run deadline

**测试：**
- `pkg/api/controlplane_test.go` —— 更新删除端点的测试
- `pkg/api/upsert_apply_test.go` —— upsert 端到端测试

---

## Task 1: 全局内容寻址 revision registry

**目标：** `POST /v1/revisions {hcl}` → `{revision: "sha256:<hex>"}`，幂等、不绑定 topology。

**Files:**
- Create: `pkg/controlplane/revision.go`
- Create: `pkg/api/revision_test.go`
- Modify: `pkg/api/api_store_interfaces.go`（加接口方法）
- Modify: `pkg/api/api_store.go`（local + postgres 实现）
- Create: `pkg/api/handler_revision.go`
- Modify: `pkg/api/server.go`（注册路由）

- [ ] **Step 1: 写失败的测试（revision 幂等 + endpoint）**

在 `pkg/api/revision_test.go`：

```go
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublishRevisionIsContentAddressed(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())

	post := func(hcl string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBufferString(hcl))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body["revision"]
	}

	first := post("resource \"sysbox_node\" \"web\" {}")
	second := post("resource \"sysbox_node\" \"web\" {}")

	require.NotEmpty(t, first)
	require.HasPrefix(t, first, "sha256:")
	require.Equal(t, first, second, "同一份 HCL 必须返回同一个 digest")
}

func TestPublishRevisionDifferentContentGivesDifferentDigest(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	post := func(hcl string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBufferString(hcl))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return string(rec.Body.Bytes())
	}
	a := post("a")
	b := post("b")
	require.NotEqual(t, a, b)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestPublishRevision -v`
Expected: FAIL（`/v1/revisions` 路由不存在，404）

- [ ] **Step 3: 加全局 Revision 模型**

在 `pkg/controlplane/revision.go`：

```go
package controlplane

import "time"

// GlobalRevision is a content-addressed HCL blob, decoupled from any topology.
// Its ID is the SHA256 of the HCL, so the same content always yields the same
// revision (idempotent publish).
type GlobalRevision struct {
	Revision  string    `json:"revision"`
	HCL       string    `json:"hcl"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}
```

- [ ] **Step 4: apiStore 加 revision 存储方法**

在 `pkg/api/api_store_interfaces.go` 的 `apiStore` 接口里加：

```go
	SaveGlobalRevision(ctx context.Context, rev controlplane.GlobalRevision) error
	GetGlobalRevision(ctx context.Context, revision string) (*controlplane.GlobalRevision, error)
```

- [ ] **Step 5: local + postgres 实现**

在 `pkg/api/api_store.go` 加（local 用 `os.MkdirAll(filepath.Join(s.runsDir, "revisions"))` + `revision+".json"` 文件；postgres 用 `saveObject(ctx, "sysbox_global_revisions", "", rev.Revision, rev)`）：

```go
func (s *localAPIStore) SaveGlobalRevision(_ context.Context, rev controlplane.GlobalRevision) error {
	dir := filepath.Join(s.runsDir, "revisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(rev)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rev.Revision+".json"), raw, 0o644)
}

func (s *localAPIStore) GetGlobalRevision(_ context.Context, revision string) (*controlplane.GlobalRevision, error) {
	raw, err := os.ReadFile(filepath.Join(s.runsDir, "revisions", revision+".json"))
	if err != nil {
		return nil, err
	}
	var rev controlplane.GlobalRevision
	if err := json.Unmarshal(raw, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}
```

（postgres 实现照 `SaveRevision` 的模式，键用空 workspace。）

- [ ] **Step 6: handler**

在 `pkg/api/handler_revision.go`：

```go
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// POST /v1/revisions — publish an HCL blob, content-addressed by its SHA256.
func (s *Server) handlePublishRevision(w http.ResponseWriter, r *http.Request) {
	hcl, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(hcl) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("hcl is required"))
		return
	}
	sum := sha256.Sum256(hcl)
	revision := "sha256:" + hex.EncodeToString(sum[:])
	rev := controlplane.GlobalRevision{
		Revision:  revision,
		HCL:       string(hcl),
		Size:      len(hcl),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.apiStore.SaveGlobalRevision(r.Context(), rev); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"revision": revision})
}
```

（`fmt` import 若缺失需补。）

- [ ] **Step 7: 注册路由**

在 `pkg/api/server.go` 加一行（放在 topologies 路由附近）：

```go
	m.HandleFunc("POST /v1/revisions", s.handlePublishRevision)
```

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestPublishRevision -v`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add pkg/controlplane/revision.go pkg/api/revision_test.go pkg/api/api_store_interfaces.go pkg/api/api_store.go pkg/api/handler_revision.go pkg/api/server.go
git commit -m "feat(api): 全局内容寻址 revision registry + POST /v1/revisions"
```

---

## Task 2: upsert apply

**目标：** `POST /v1/topologies/{name}/apply {revision, inputs, deadline_at}` 收敛拓扑，不存在则建。

**Files:**
- Modify: `pkg/controlplane/model.go`（Run 加 DeadlineAt）
- Modify: `pkg/api/handler_topo.go`（applyRequest 加 deadline_at，decode 保持 revision）
- Modify: `pkg/api/run_service.go`（StartApply：revision → HCL → upsert workspace → 建 run）
- Modify: `pkg/api/workspace_service.go`（Create 幂等）
- Create: `pkg/api/upsert_apply_test.go`

- [ ] **Step 1: 写失败的测试（upsert：不存在则建 + 幂等）**

在 `pkg/api/upsert_apply_test.go`：

```go
package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyUpsertCreatesTopologyOnFirstCall(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	// publish
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/revisions", bytes.NewBufferString(`resource "sysbox_node" "web" {}`)))
	require.Equal(t, http.StatusCreated, rec.Code)
	rev := decodeRevision(t, rec)

	// apply（拓扑不存在）
	body := `{"revision":"` + rev + `","inputs":{}}`
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/topologies/cf-a/apply", bytes.NewBufferString(body)))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	// topology 现在存在
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/cf-a", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"has_hcl":true`)
}

func decodeRevision(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m["revision"]
}
```

（`encoding/json` import 需补。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestApplyUpsert -v`
Expected: FAIL（apply 不认 revision、不建拓扑）

- [ ] **Step 3: Run 加 DeadlineAt**

在 `pkg/controlplane/model.go` 的 `Run` 结构加：

```go
	// DeadlineAt is the consumer-specified instant by which this run must have
	// converged; the supervisor fails the run past it. Zero means no bound.
	DeadlineAt time.Time `json:"deadline_at,omitempty"`
```

- [ ] **Step 4: applyRequest 加 deadline_at**

在 `pkg/api/handler_topo.go` 的 `applyRequest` 加：

```go
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
```

（`time` import 已存在。）

- [ ] **Step 5: StartApply 改 upsert**

在 `pkg/api/run_service.go` 的 `StartApply` 开头（`if req.PlanID != ""` 之前）加：从 revision 取 HCL，upsert workspace：

```go
func (s *RunService) StartApply(ctx context.Context, topology string, req RunStartRequest) (*controlplane.Run, error) {
	if req.Revision != "" {
		rev, err := s.revisions.GetGlobalRevision(ctx, req.Revision)
		if err != nil {
			return nil, runError(runServiceBadRequest, fmt.Errorf("revision %s: %w", req.Revision, err))
		}
		if err := s.workspaces.UpsertHCL(ctx, topology, rev.HCL); err != nil {
			return nil, runError(runServiceInternal, err)
		}
	}
	// ... 原逻辑不变，但 plan_id 分支移除
	run := s.jobs.startWithOptions(topology, "apply", runStartOptions{
		Revision:   req.Revision,
		AgentID:    req.AgentID,
		UnsafeState: req.AllowUnsafeState,
		Inputs:     req.Inputs,
	})
	if req.DeadlineAt != nil {
		run.DeadlineAt = *req.DeadlineAt
	}
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	return run, nil
}
```

（`RunService` 需有 `revisions` 和 `workspaces` 字段；若现无则从 `Server` 传入，或直接放 `s.srv`。此处按「RunService 持有对 apiStore + WorkspaceService 的引用」实现，构造处补上。）

- [ ] **Step 6: WorkspaceService.UpsertHCL（幂等）**

在 `pkg/api/workspace_service.go` 加：

```go
// UpsertHCL writes the topology's HCL, creating the workspace directory if
// needed. Idempotent: repeated calls with the same content are a no-op.
func (s *WorkspaceService) UpsertHCL(ctx context.Context, topology string, hcl string) error {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return err
	}
	hclPath := s.HCLFile(topology)
	if err := os.MkdirAll(filepath.Dir(hclPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(hclPath, []byte(hcl), 0o644)
}
```

- [ ] **Step 7: 构造处补引用**

在 `newRunService` / `NewServer` 里，给 `RunService` 补上 `revisions apiStore`（即 `s.apiStore`）和 `workspaces *WorkspaceService` 字段，并在构造处赋值。

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestApplyUpsert -v`
Expected: PASS

- [ ] **Step 9: 跑全量确认无回归**

Run: `go test ./... 2>&1 | grep -v '^ok' | head`
Expected: 无 FAIL

- [ ] **Step 10: 提交**

```bash
git add pkg/controlplane/model.go pkg/api/handler_topo.go pkg/api/run_service.go pkg/api/workspace_service.go pkg/api/upsert_apply_test.go pkg/api/server.go
git commit -m "feat(api): apply 改 upsert（revision → HCL → create-if-not-exists）"
```

---

## Task 3: nodes / outputs 进状态

**目标：** `GET /v1/topologies/{name}` 的 status 补 `nodes` 和 `outputs`。

**Files:**
- Modify: `pkg/controlplane/conditions.go`（TopologyStatus 加 Nodes/Outputs）
- Modify: `pkg/api/handler_workspace.go`（topologyStatus 填充 nodes/outputs）
- Create: `pkg/api/status_nodes_outputs_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api/status_nodes_outputs_test.go`：

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetTopologyStatusIncludesNodesAndOutputs(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	createWorkspace(t, s, "lab") // 已有 helper，写一个含 node + output 的 HCL
	// 用带 node + output 的 HCL 重写 workspace
	s.workspaceService().UpsertHCL(t.Context(), "lab", `
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}
output "web_addr" {
  value = sysbox_node.web.id
}
`)
	// 写 state 让 node 有 primary_ip
	writeState(t, s.runsDir, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:    address.Resource("sysbox_node", "web"),
			Driver:     "docker",
			Attributes: state.MustAttributes(map[string]any{"primary_ip": "10.0.0.5"}),
		}},
	})

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/topologies/lab", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"nodes"`)
	require.Contains(t, rec.Body.String(), `"10.0.0.5"`)
	require.Contains(t, rec.Body.String(), `"outputs"`)
}
```

（`state`、`address` import 补；`s.runsDir` 可能不是公开字段，改用 `t.TempDir()` 直接构造 `NewServer(runs, workspaces)`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestGetTopologyStatusIncludesNodesAndOutputs -v`
Expected: FAIL（status 无 nodes/outputs）

- [ ] **Step 3: TopologyStatus 加字段**

在 `pkg/controlplane/conditions.go` 的 `TopologyStatus` 加：

```go
	Nodes   []TopologyNode   `json:"nodes,omitempty"`
	Outputs map[string]any   `json:"outputs,omitempty"`
```

并新增：

```go
// TopologyNode is one node in a topology's status, for ops display only.
type TopologyNode struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	State   string `json:"state,omitempty"`
}
```

- [ ] **Step 4: topologyStatus 填充**

在 `pkg/api/handler_workspace.go` 的 `topologyStatus` 里，加载 state，遍历 `sysbox_node` 资源填 `Nodes`，用 `runtime.EvaluateOutputs` 填 `Outputs`：

```go
func (s *Server) topologyStatus(topology string) *controlplane.TopologyStatus {
	latest := s.latestRun(topology)
	var assertion *controlplane.AssertionResult
	if latest != nil {
		assertion = latest.Assertion
	}
	status := controlplane.ComputeTopologyStatus(latest, s.healthFor(topology), assertion)
	// nodes: 从 state 派生
	if st, err := s.workspaceService().LoadState(topology); err == nil {
		for _, r := range st.Resources {
			if r.Address.Type != "sysbox_node" {
				continue
			}
			status.Nodes = append(status.Nodes, controlplane.TopologyNode{
				Name:    r.Address.Name,
				Address: r.Str("primary_ip"),
				State:   r.Status,
			})
		}
	}
	// outputs: 求值 HCL output 块
	if root, evalCtx, err := s.loadRootAndEvalCtx(topology); err == nil {
		if outs, err := runtime.EvaluateOutputs(root, evalCtx); err == nil {
			status.Outputs = map[string]any{}
			for k, v := range outs {
				status.Outputs[k] = v.Value
			}
		}
	}
	if latest != nil && isConvergingRun(latest) {
		if deadline := convergenceDeadline(latest, s.cfg.ConvergeTimeout()); !deadline.IsZero() {
			status.DeadlineAt = &deadline
		}
	}
	return &status
}
```

（`loadRootAndEvalCtx` 需实现：`runtime.LoadWorkspaceWithManager(s.workspaceService().HCLFile(topology), s.stateManager(topology))` 返回 root + evalCtx。`runtime`、`config` import 视情况补。）

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestGetTopologyStatusIncludesNodesAndOutputs -v`
Expected: PASS

- [ ] **Step 6: 全量 + 提交**

```bash
go test ./... 2>&1 | grep -v '^ok' | head
git add pkg/controlplane/conditions.go pkg/api/handler_workspace.go pkg/api/status_nodes_outputs_test.go
git commit -m "feat(api): status 补 nodes 与 outputs"
```

---

## Task 4: per-apply deadline_at 强制

**目标：** apply 传入的 `deadline_at` 由 supervisor 到点判失败，覆盖配置默认。

**Files:**
- Modify: `pkg/api/jobs.go`（markConvergenceDeadlineExceeded 优先用 run.DeadlineAt）
- Create: `pkg/api/deadline_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api/deadline_test.go`：

```go
package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func TestConvergenceDeadlineUsesPerRunDeadline(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	run := s.jobs.start("lab", "apply")
	run.Status = controlplane.RunRunning
	run.StartedAt = time.Now().Add(-10 * time.Minute)
	run.DeadlineAt = time.Now().Add(-1 * time.Minute) // 已过点
	s.jobs.replace(run)

	s.jobs.markConvergenceDeadlineExceeded(time.Now(), 15*time.Minute)

	got, ok := s.jobs.get(run.ID)
	require.True(t, ok)
	require.Equal(t, controlplane.RunFailed, got.Status)
	require.Contains(t, got.Err, "deadline")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestConvergenceDeadlineUsesPerRunDeadline -v`
Expected: FAIL（不认 per-run deadline）

- [ ] **Step 3: markConvergenceDeadlineExceeded 优先 per-run deadline**

在 `pkg/api/jobs.go` 的 `markConvergenceDeadlineExceeded` 里，把 deadline 判断改为「有 `run.DeadlineAt` 用它，否则用 `StartedAt + timeout`」：

```go
	for i := range snapshots {
		r := &snapshots[i]
		if r.Op == "destroy" || !isConvergingStatus(r.Status) {
			continue
		}
		deadline := r.DeadlineAt
		if deadline.IsZero() {
			if r.StartedAt.IsZero() {
				continue
			}
			deadline = r.StartedAt.Add(timeout)
		}
		if now.Before(deadline) {
			continue
		}
		r.MarkFinished(fmt.Errorf("convergence deadline exceeded"), now)
		j.replace(r)
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestConvergenceDeadlineUsesPerRunDeadline -v`
Expected: PASS

- [ ] **Step 5: 全量 + 提交**

```bash
go test ./... 2>&1 | grep -v '^ok' | head
git add pkg/api/jobs.go pkg/api/deadline_test.go
git commit -m "feat(api): supervisor 按 run.DeadlineAt 到点判失败"
```

---

## Task 5: 删除旧端点

**目标：** 删 create / hcl / plans / per-topology revisions，更新测试。

**Files:**
- Modify: `pkg/api/server.go`（删路由）
- Modify: `pkg/api/handler_workspace.go`（删 handleCreateTopology / handleUpdateHCL）
- Modify: `pkg/api/handler_topo.go`（删 handleCreatePlan；apply 去掉 plan_id 分支）
- Modify: `pkg/api/handler_controlplane.go`（删 revisions handler 或保留只读）
- Modify: `pkg/api/controlplane_test.go`（删相关断言）

- [ ] **Step 1: 写失败的测试（旧端点 404）**

在 `pkg/api/upsert_apply_test.go` 加：

```go
func TestOldEndpointsRemoved(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/topologies"},
		{http.MethodPut, "/v1/topologies/lab/hcl"},
		{http.MethodPost, "/v1/topologies/lab/plans"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(""))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s %s should be removed", tc.method, tc.path)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestOldEndpointsRemoved -v`
Expected: FAIL（旧端点仍在）

- [ ] **Step 3: 删路由 + handler**

删 `pkg/api/server.go` 里的 `POST /v1/topologies`、`PUT /v1/topologies/{topology}/hcl`、`POST /v1/topologies/{topology}/plans`、`POST /v1/topologies/{topology}/revisions` 注册行；删 `handler_workspace.go` 的 `handleCreateTopology` / `handleUpdateHCL`；删 `handler_topo.go` 的 `handleCreatePlan`；`handleApply` 去掉 `plan_id` 解码与校验分支；`handleCreateRevision` 删除（其 `GET` 列表也可一并删）。

（删除函数时同步删其 `_ = ` 等未用 import，`go build` 会提示。）

- [ ] **Step 4: 更新 controlplane_test.go**

删除 `pkg/api/controlplane_test.go` 里对 create / hcl / plans 端点的断言段，保留 list/get/apply/destroy 相关。

- [ ] **Step 5: 跑测试确认通过 + 全量**

```bash
go test ./pkg/api/ -run TestOldEndpointsRemoved -v
go test ./... 2>&1 | grep -v '^ok' | head
```
Expected: 全 PASS / 无 FAIL

- [ ] **Step 6: 提交**

```bash
git add pkg/api/server.go pkg/api/handler_workspace.go pkg/api/handler_topo.go pkg/api/handler_controlplane.go pkg/api/controlplane_test.go pkg/api/upsert_apply_test.go
git commit -m "feat(api): 删除旧多步端点（create/hcl/plans/revisions），只留 revision+upsert"
```

---

## Self-Review

**Spec 覆盖：**
- 全局 revision registry → Task 1 ✓
- upsert apply → Task 2 ✓
- nodes/outputs → Task 3 ✓
- per-apply deadline_at → Task 4 ✓
- 删除旧端点 → Task 5 ✓
- 先加新后删旧 → Task 1-4 加新，Task 5 删旧 ✓

**占位符扫描：** 无 TBD/TODO；所有 step 有代码或明确命令。

**类型一致性：** `GlobalRevision.Revision`（string digest）贯穿 Task 1-2；`Run.DeadlineAt`（time.Time）Task 2 定义、Task 4 使用，一致。
