# 架构硬化（8 项）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 sysbox 架构复盘里的 8 项硬化（4 代码 + 4 文档/测试），排除 cty 贯穿。

**Architecture:** 每项独立、低风险。代码项：#3 函数去重、#6 projection 持久化、#9 配置校验、#10 泄漏测试。文档项：#2 var/secret 边界、#5 inputs 重启窗口、#7 state 迁移、#8 多实例。

**Tech Stack:** Go、testify、net/http、postgres/sqlite store。

---

## Task 1: #3 函数注册去重

**Files:**
- Modify: `pkg/config/eval.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/config/cidr_functions_test.go` 加：

```go
func TestBaseFunctionsIncludesAllBuiltins(t *testing.T) {
	fns := baseFunctions()
	for _, name := range []string{"env", "env_optional", "toset", "cidrsubnet", "cidrhost"} {
		_, ok := fns[name]
		require.True(t, ok, "missing builtin %q", name)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/config/ -run TestBaseFunctionsIncludesAllBuiltins -v`
Expected: FAIL — `baseFunctions` undefined.

- [ ] **Step 3: 实现 `baseFunctions` 并替换三处 map**

在 `pkg/config/eval.go` 顶部附近加：

```go
func baseFunctions() map[string]function.Function {
	return map[string]function.Function{
		"env":          envFunc,
		"env_optional": envOptionalFunc,
		"toset":        tosetFunc,
		"cidrsubnet":   cidrsubnetFunc,
		"cidrhost":     cidrhostFunc,
	}
}
```

把三处 `Functions` map（约 65、90、164 行）统一改为 `Functions: baseFunctions(),`。第 65/90 行的单行形式与第 164 行的多行形式都要改。

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `go test ./pkg/config/ -run TestBaseFunctions -v && go test ./pkg/config/... -count=1`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/config/eval.go pkg/config/cidr_functions_test.go
git commit -m "refactor(config): 内置函数注册去重为 baseFunctions()"
```

---

## Task 2: #5 敏感 inputs 重启窗口（文档 + 测试）

**Files:**
- Modify: `pkg/controlplane/model.go`（`Run.Inputs` 注释）
- Modify: `pkg/api/api_store.go`（`markInterruptedRuns` 附近注释）
- Test: `pkg/api/api_store_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api/api_store_test.go` 加：

```go
func TestMarkInterruptedRunsFailsInFlightRun(t *testing.T) {
	runs := []controlplane.Run{
		{ID: "r1", Status: controlplane.RunRunning, Inputs: map[string]string{"flag": "canary"}},
	}
	out := markInterruptedRuns(runs)
	require.Equal(t, controlplane.RunFailed, out[0].Status)
	require.True(t, out[0].Recoverable)
}
```

- [ ] **Step 2: 跑测试确认失败**（若 `markInterruptedRuns` 行为已符合，则此测试直接 PASS——确认它确实 PASS 即为「锁定」，无需改实现；只补文档）

Run: `go test ./pkg/api/ -run TestMarkInterruptedRunsFailsInFlightRun -v`
Expected: PASS（`markInterruptedRuns` 已把 Running→Failed+Recoverable）。

- [ ] **Step 3: 文档化 transient 语义**

在 `pkg/controlplane/model.go` 的 `Run.Inputs` 字段注释（约 100-103 行）补一句：「inputs are transient: not durably persisted, and an in-flight run is failed by `markInterruptedRuns` on restart, so secret plaintext never survives a restart.」

- [ ] **Step 4: 提交**

```bash
git add pkg/api/api_store_test.go pkg/controlplane/model.go
git commit -m "docs(controlplane): 明确 inputs transient + 重启 fail in-flight run 语义"
```

---

## Task 3: #10 泄漏自动化防线

**Files:**
- Create: `pkg/api/sensitive_input_leak_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `pkg/api/sensitive_input_leak_test.go`，用一个固定 canary 跑一次 apply，然后遍历 store 里所有持久化对象、断言 canary 不出现：

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

const leakCanary = "plaintext-canary-must-not-leak"

func TestSensitiveInputNeverPersisted(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir, dir)

	// apply with a canary input
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/revisions", nil))
	_ = rec

	// — 实际实现按现有 apply 测试 harness 构造（参考 upsert_apply_test.go），
	//   用 publish + apply 带 inputs={"flag": leakCanary} 跑通。

	// 遍历 store 里所有可读的持久化对象，逐一 json.Marshal 后断言不含 canary
	all := s.apiStore.(interface{ DumpAllJSON() []string }) // 见 Step 2 说明
	for _, raw := range all.DumpAllJSON() {
		require.NotContains(t, raw, leakCanary)
	}
}
```

（注：上面的 `DumpAllJSON` 是占位说明——实际实现见 Step 3，改为遍历具体的 store 读方法。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestSensitiveInputNeverPersisted -v`
Expected: 编译失败或 FAIL（canary 出现在某处 / 测试逻辑不完整）。

- [ ] **Step 3: 实现真实的遍历**

不要用 `DumpAllJSON`。改为在测试里显式调用 store 的读方法，逐一序列化并断言。参考 `pkg/api/sensitive_input_persistence_test.go` 里已有的 store 访问方式（`s.apiStore` 的 `LoadRuns`、`GetRun`、`LoadHealth` 等），把每个返回的结构体 `json.Marshal` 后 `require.NotContains(canary)`。至少覆盖：runs（`LoadRuns`）、health snapshot（`LoadHealth`）、state（`workspaceService().LoadState` 后 `Marshal`）。

具体用 `s.apiStore` 的现有方法（读 `pkg/api/api_store_interfaces.go` 确认可用的 `LoadXxx`），不要在接口里加新方法。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestSensitiveInputNeverPersisted -v`
Expected: PASS（canary 不泄漏）。

- [ ] **Step 5: 全量 + 提交**

Run: `go test ./pkg/api/... -count=1`
```bash
git add pkg/api/sensitive_input_leak_test.go
git commit -m "test(api): canary 全局泄漏探测兜底测试"
```

---

## Task 4: #6 projection 持久化

**Files:**
- Modify: `pkg/api/api_store_interfaces.go`（加 `projectionStore` 接口 + embed）
- Modify: `pkg/api/api_store.go`（local + postgres 实现）
- Modify: `pkg/api/api_store_sqlite.go`（sqlite 实现）
- Modify: `pkg/api/handler_agent.go`（`handlePostAgentResourceProjection` 双写）
- Modify: `pkg/api/handler_resource.go`（`authoritativeTopologyHealth` 读 store）
- Test: `pkg/api/api_store_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api/api_store_test.go` 加：

```go
func TestResourceProjectionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir, dir)
	proj := controlplane.ResourceProjection{
		AgentID: "agent-1", Topology: "web", ObservedAt: time.Now().UTC(),
		Resources: []controlplane.ResourceHealth{{Resource: "sysbox_node.web", Status: controlplane.ResourceHealthHealthy}},
	}
	require.NoError(t, s.apiStore.SaveResourceProjection(context.Background(), proj))
	got, err := s.apiStore.LoadResourceProjection(context.Background(), "web")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "agent-1", got.AgentID)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestResourceProjectionStoreRoundTrip -v`
Expected: FAIL — `SaveResourceProjection`/`LoadResourceProjection` 未定义。

- [ ] **Step 3: 加接口**

在 `pkg/api/api_store_interfaces.go` 的 `healthStore` 附近加：

```go
type projectionStore interface {
	SaveResourceProjection(ctx context.Context, proj controlplane.ResourceProjection) error
	LoadResourceProjection(ctx context.Context, topology string) (*controlplane.ResourceProjection, error)
}
```

并把它 embed 进 `apiStore`（参照 `healthStore` 的 embed）。

- [ ] **Step 4: 三后端实现（照抄 healthStore）**

- **local**（`api_store.go`，照抄 `SaveHealth`/`LoadHealth` 177-207 行）：`SaveResourceProjection` 写 `filepath.Join(s.runsDir, proj.Topology, "projection.json")`（`json.MarshalIndent`），`LoadResourceProjection` 读同一文件，not-exist 返回 `nil, nil`（区别于 health 的 error，因为 projection 可能还没上报过）。
- **postgres**（`api_store.go`，照抄 477-528 行）：建表 `sysbox_projection (topology text primary key, data jsonb, updated_at timestamptz)`（在 migrations 里加一个 version，`apiSchemaVersion` +1 并保持 `TestAPIMigrationsMatchSchemaVersion` 一致），`SaveResourceProjection`/`LoadResourceProjection` 照抄 `SaveHealth`/`LoadHealth` 的 SQL，`pgx.ErrNoRows` 返回 `nil, nil`。
- **sqlite**（`api_store_sqlite.go`，照抄 504-542 行）：建表 `sysbox_projection (topology text primary key, data text)`（在 `ensureSchema` 里 `CREATE TABLE IF NOT EXISTS`），`SaveResourceProjection`/`LoadResourceProjection` 照抄，`sql.ErrNoRows` 返回 `nil, nil`。

- [ ] **Step 5: 双写 + 读 store**

- `handlePostAgentResourceProjection`（`handler_agent.go:300`）：在 `s.agents.SaveResourceProjection(req)` 之后加 `_ = s.apiStore.SaveResourceProjection(r.Context(), req)`（持久化失败打日志、不阻断上报）。
- `authoritativeTopologyHealth`（`handler_resource.go:36`）：把「读内存 `s.agents.ListResourceProjections`」改为「读 `s.apiStore.LoadResourceProjection`」，回退 docker inspect。

- [ ] **Step 6: 跑测试确认通过 + 全量**

Run: `go test ./pkg/api/ -run TestResourceProjectionStoreRoundTrip -v && go test ./pkg/api/... -count=1`
Expected: 全 PASS。

- [ ] **Step 7: 提交**

```bash
git add pkg/api/api_store_interfaces.go pkg/api/api_store.go pkg/api/api_store_sqlite.go pkg/api/handler_agent.go pkg/api/handler_resource.go pkg/api/api_store_test.go
git commit -m "feat(api): ResourceProjection 持久化，重启后仍可用"
```

---

## Task 5: #9 配置校验

**Files:**
- Modify: `pkg/api/api_store.go`（postgres 连接校验）
- Modify: `pkg/api/server.go`（启动校验）

- [ ] **Step 1: 写失败的测试**（若 postgres 不可达难以在测试里复现，则本项退化为「实现 + 文档」，测试仅验证 postgres store 初始化在 DSN 非法时返回错误）

在 `pkg/api/api_store_test.go` 加（若 `isolatedPostgresTestDSN` harness 可用则用之）：

```go
func TestPostgresAPIStoreRejectsUnreachableDSN(t *testing.T) {
	_, err := newAPIStore(t.TempDir(), "postgres://127.0.0.1:1/nonexistent")
	require.Error(t, err)
}
```

（若 `newAPIStore` 当前不校验连接，此测试会 FAIL，见 Step 2。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestPostgresAPIStoreRejectsUnreachableDSN -v`
Expected: FAIL 或无法连接时错误未在构造期返回。

- [ ] **Step 3: 实现启动校验**

在 `newAPIStore`（`api_store.go`）里，当 backend 为 postgres 时，构造后立刻做一次 `ping`（`conn.Ping(ctx)`）校验可达，不可达返回 error。若 `newAPIStore` 是惰性连接（`connect` 时才连），则在 `NewServer`（`server.go:62` 附近）构造 store 后加一次显式 ping，失败则返回 error 而非静默降级。

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `go test ./pkg/api/... -count=1`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/api/api_store.go pkg/api/server.go pkg/api/api_store_test.go
git commit -m "feat(api): 启动时校验 state backend 可达"
```

---

## Task 6: #2/#7/#8 架构约定文档

**Files:**
- Create: `docs/design/2026-09-07-arch-conventions.md`

- [ ] **Step 1: 写文档**

创建 `docs/design/2026-09-07-arch-conventions.md`，三节：

1. **变量 vs 秘密边界**（#2）：`var.<name>` 是构建期参数化（build 期求值，可进 count/for_each/资源字段）；`secret://input/<name>` 是运行期秘密（exec 期物化，明文不进 state/outputs/日志）。规则：需要「参数化拓扑结构」用 var；需要「注入 canary/flag 等秘密」用 secret。
2. **state 不迁移**（#7）：state JSON 的 `SchemaVersion` 升级即破坏旧 payload，不提供迁移。当前无生产数据，这是刻意取舍；一旦有存量 state，升级前需另做迁移方案。
3. **单实例假设**（#8）：apply 的并发合流依赖进程内 `lockTopology` + 内存 `j.runs` 的确定性 operationKey。多实例（多 API server 共享 store）需要 store 级 `RequestFingerprint` 去重（destroy 已有、apply 没有），当前不在范围内。

- [ ] **Step 2: 提交**

```bash
git add docs/design/2026-09-07-arch-conventions.md
git commit -m "docs: 架构约定（var/secret 边界、state 不迁移、单实例假设）"
```

---

## Self-Review

**Spec 覆盖：** #3→Task1、#5→Task2、#10→Task3、#6→Task4、#9→Task5、#2/#7/#8→Task6。全部覆盖。

**占位符扫描：** Task 3 的 Step 1 用 `DumpAllJSON` 是显式标注「占位说明，见 Step 3」，Step 3 给了真实实现（遍历 store 读方法）。Task 4/5 的 postgres/sqlite 用「照抄 healthStore + 明确文件行号」，给了明确的照抄目标。

**类型一致性：** `SaveResourceProjection(ctx, controlplane.ResourceProjection) error` / `LoadResourceProjection(ctx, topology) (*controlplane.ResourceProjection, error)` 在 Task 4 的接口、实现、测试一致。
