# sysbox 六项缺口修复 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复多节点拓扑联调发现的 4 个缺口 + 2 个触类旁通（locals/count 缺 substrate），见 `docs/design/2026-09-07-sysbox-gaps-design.md`。

**Architecture:** 六个独立、低风险修复：#1 compose 加 pid:host+caps；#2 统一 eval 上下文；#3 file source 相对 workspace；#4 destroy 同步回收。

**Tech Stack:** Go、hcl/v2、cty、testify、docker compose。

---

## Task 1: #1 firewall —— agent 加 `pid: host` + caps

**Files:**
- Modify: `deploy/docker/compose.agent.yml`

- [ ] **Step 1: 改 compose**

在 `sysbox-agent` 服务的 `container_name` 之后、`restart` 之前（或任意 service 级字段处）插入：

```yaml
    pid: host
    cap_add:
      - NET_ADMIN
      - SYS_ADMIN
```

- [ ] **Step 2: 校验 YAML 合法**

Run: `docker compose -f deploy/docker/compose.agent.yml config -q`
Expected: 无输出（合法）。

- [ ] **Step 3: 提交**

```bash
git add deploy/docker/compose.agent.yml
git commit -m "fix(deploy): agent 加 pid:host + NET_ADMIN/SYS_ADMIN 使 firewall 可用"
```

---

## Task 2: #2 + #2c + #2d —— locals / count 求值上下文

**Files:**
- Modify: `pkg/config/eval.go`（`buildEvalContextInner`）
- Test: `pkg/config/eval_diagnostics_test.go` 或新建 `pkg/config/eval_locals_test.go`

- [ ] **Step 1: 写失败的测试**

新建 `pkg/config/eval_locals_test.go`：

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// locals 可以引用 var.*、先声明的 local.*、substrate.*。
func TestLocalsReferenceVarAndEarlierLocalAndSubstrate(t *testing.T) {
	root, err := ParseString(`
variable "team_index" { type = number }
substrate "docker" "local" {}

locals {
  subnet = cidrsubnet("10.200.0.0/16", 8, var.team_index)
  web_ip = cidrhost(local.subnet, 10)
  which  = substrate.docker.local
}
resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = "alpine"
}
`, "locals.hcl")
	require.NoError(t, err)

	ctx, err := BuildEvalContextWithInputs(root, "", map[string]string{"team_index": "3"})
	require.NoError(t, err)

	locals := ctx.Variables["local"]
	require.Equal(t, "10.200.3.0/24", locals.GetAttr("subnet").AsString())
	require.Equal(t, "10.200.3.10", locals.GetAttr("web_ip").AsString())
	require.Equal(t, "local", locals.GetAttr("which").AsString())
}

// count 可以引用 substrate.*。
func TestCountReferencesSubstrate(t *testing.T) {
	root, err := ParseString(`
substrate "docker" "local" {}
locals { n = substrate.docker.local == "local" ? 2 : 0 }
resource "sysbox_node" "web" {
  count     = local.n
  substrate = substrate.docker.local
  image     = "alpine"
}
`, "count.hcl")
	require.NoError(t, err)

	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	nodes := ctx.Variables["sysbox_node"].GetAttr("web")
	require.True(t, nodes.Type().IsTupleType(), "count-expanded resource should be a tuple, got %s", nodes.Type().FriendlyName())
	require.Equal(t, int64(2), nodes.LengthInt())
}
```

> 注：`substrate.docker.local` 的值是 substrate 的 `alias`（见 `buildEvalContextInner` 顶部：`subTypes[sb.Type][alias] = cty.StringVal(sb.Type)`）。这里 `alias="local"`、`Type="docker"`，所以值应为 `"docker"` 还是 `"local"`？**实现前先确认**：`substrateVal[typ] = ObjectVal(byAlias)`，`byAlias[alias] = StringVal(sb.Type)`，即 `substrate.docker.local == "docker"`。上面测试里的断言以**实现前的实际语义为准**，写测试时先 `go test` 跑一次打印实际值再定断言。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/config/ -run 'TestLocalsReference|TestCountReferencesSubstrate' -v`
Expected: FAIL（`Variables not allowed` / count 无法引用 substrate）。

- [ ] **Step 3: 实现 —— locals 增量求值 + substrate 注入**

改 `pkg/config/eval.go` 的 `buildEvalContextInner`：

1. 在 `substrateVal` 算好后，构造 locals 求值上下文（替换原 `localCtx` 单行）：

```go
	// locals 可以引用 var.*、先声明的 local.*、substrate.*（对齐 Terraform）。
	// 按源码顺序逐个求值，边求边把已求出的 local 加入 local 对象。
	localCtx := &hcl.EvalContext{
		Variables: map[string]cty.Value{},
		Functions: baseFunctions(),
	}
	if len(varBindings) > 0 {
		localCtx.Variables["var"] = cty.ObjectVal(varBindings)
	}
	if len(substrateVal) > 0 {
		localCtx.Variables["substrate"] = cty.ObjectVal(substrateVal)
	}
	localVals := map[string]cty.Value{}
	for _, lb := range root.Locals {
		if lb.Remain == nil {
			continue
		}
		attrs, diags := lb.Remain.JustAttributes()
		if diags.HasErrors() {
			diagnostics = append(diagnostics, fromHCLDiagnostics(diags)...)
			continue
		}
		// 按 NameRange 排序，保证先声明的 local 先求值。
		names := make([]string, 0, len(attrs))
		for name := range attrs {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			return attrs[names[i]].NameRange.Start.Byte < attrs[names[j]].NameRange.Start.Byte
		})
		for _, name := range names {
			if len(localVals) > 0 {
				localCtx.Variables["local"] = cty.ObjectVal(localVals)
			}
			val, valueDiags := attrs[name].Expr.Value(localCtx)
			if valueDiags.HasErrors() {
				diagnostics = append(diagnostics, fromHCLDiagnostics(valueDiags)...)
				continue
			}
			localVals[name] = val
		}
	}
```

2. `preCtx` 补 substrate（在原 `if len(varBindings) > 0` 块后加）：

```go
	if len(substrateVal) > 0 {
		preCtx.Variables["substrate"] = cty.ObjectVal(substrateVal)
	}
```

> 注意：原 `localCtx` 在函数里 `substrateVal` 之后定义；`sort` 已在上方 import。确认 `preCtx` 变量在原代码 line 101，`substrateVal` 在 line 71-74 已算好，作用域可用。

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `go test ./pkg/config/ -run 'TestLocalsReference|TestCountReferencesSubstrate' -v && go test ./pkg/config/... -count=1`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/config/eval.go pkg/config/eval_locals_test.go
git commit -m "fix(config): locals/count 求值上下文补 var + 先声明 local + substrate"
```

---

## Task 3: #3 —— provisioner `file` 相对 source 相对 workspace

**Files:**
- Modify: `pkg/runtime/executor.go`
- Modify: `pkg/runtime/resource_node.go`（`runProvisioners` file 分支）
- Modify: `pkg/agentexec/executor.go`（三处注入 workspaceDir）
- Test: `pkg/runtime/resource_node_test.go` 或新建 `pkg/runtime/provisioner_file_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/runtime` 包新建 `provisioner_file_test.go`（用一个 fake `substrate.Connection` 记录 CopyFile 的 src）：

```go
package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingConn struct {
	copiedSrc string
}

func (c *recordingConn) CopyFile(_ context.Context, src, _ string) error { c.copiedSrc = src; return nil }
// 其余 substrate.Connection 方法：实现时按接口补齐（返回零值即可，见 Step 1 注）。

func TestRunProvisionersFileResolvesRelativeSourceAgainstWorkspace(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(nil, nil)
	e.SetWorkspaceDir(dir)

	conn := &recordingConn{}
	err := e.runProvisioners(context.Background(), conn, []config.ProvisionerConfig{
		{Type: "file", Source: "files/edge.py", Destination: "/opt/edge.py"},
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "files", "edge.py"), conn.copiedSrc)
}
```

> 注：`substrate.Connection` 接口有多个方法（`Exec`、`ExecBackground`、`CopyFile` 等），`recordingConn` 需实现全部方法才能满足接口。实现时对照 `pkg/substrate` 的 `Connection` 接口补齐其余方法（返回 `(0, nil)` / `nil` 等零值）。若 `runProvisioners` 是 `*Executor` 未导出方法，测试放同包 `package runtime` 即可访问。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/runtime/ -run TestRunProvisionersFileResolvesRelativeSourceAgainstWorkspace -v`
Expected: FAIL（`copiedSrc` 仍是相对路径 `files/edge.py`）。

- [ ] **Step 3: 实现**

1. `pkg/runtime/executor.go`：在 `topology`/`runID` 字段旁加 `workspaceDir string`，加：

```go
// SetWorkspaceDir records the directory a topology's HCL tree is materialised
// into (workspacesDir/<topology>/). Relative provisioner "file" sources resolve
// against it; absolute and ~-expanded sources are left untouched.
func (e *Executor) SetWorkspaceDir(dir string) { e.workspaceDir = dir }
```

2. `pkg/runtime/resource_node.go` 的 `runProvisioners` file 分支，改 `src` 解析：

```go
		case "file":
			if p.Source == "" || p.Destination == "" {
				return fmt.Errorf("provisioner file: source and destination required")
			}
			src := expandTilde(p.Source)
			if !filepath.IsAbs(src) && e.workspaceDir != "" {
				src = filepath.Join(e.workspaceDir, src)
			}
			e.logf("[provisioner] file: %s → %s\n", src, p.Destination)
			if err := conn.CopyFile(ctx, src, p.Destination); err != nil {
				return fmt.Errorf("provisioner file %s: %w", src, err)
			}
```

   （确认 `resource_node.go` 已 import `path/filepath`；若无则补。）

3. `pkg/agentexec/executor.go` 三处 `exec.SetRunContext(run.Topology, run.ID)` 之后加：

```go
	exec.SetWorkspaceDir(filepath.Dir(e.bridge.HCLFile(run.Topology)))
```

   三处分别在 `executeReset`、`executeApply`、`executeDestroy`。确认文件已 import `path/filepath`。

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `go test ./pkg/runtime/ -run TestRunProvisionersFile -v && go test ./pkg/runtime/... ./pkg/agentexec/... -count=1`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/runtime/executor.go pkg/runtime/resource_node.go pkg/runtime/provisioner_file_test.go pkg/agentexec/executor.go
git commit -m "fix(runtime): provisioner file 相对 source 相对 workspace 目录解析"
```

---

## Task 4: #4 —— destroy 同步回收

**Files:**
- Modify: `pkg/api/run_service.go`
- Test: `pkg/api/run_service_test.go` 或 `pkg/api/runs_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api` 包加测试：destroy 派发后，`StartDestroy` 应等待 run 到终态（用一个 fake agent 立即回报完成），或验证 `waitForCompletion` 在 run 到终态后返回、超时返回错误。

```go
func TestStartDestroyWaitsForCompletion(t *testing.T) {
	// 构造 RunService（复用 run_service_test.go 的 harness），使 destroy run
	// 在派发后由测试立即 finish（status=done）。
	// 调用 StartDestroy，断言返回的 run.Status.IsTerminal()。
}
```

> 注：`StartDestroy` 派发 run 后，run 由 agent 异步执行并 `finish`。测试里可在派发后手动 `s.jobs.finish(run, nil)` 模拟 agent 完成，再断言 `waitForCompletion` 返回且 run 到终态。**实现前先读 `pkg/api/run_service_test.go` 确认 RunService 的构造 harness 与 `s.jobs` 的可见性**，据此写测试。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestStartDestroyWaitsForCompletion -v`
Expected: FAIL（当前 destroy 派发后立即返回，run 尚未到终态）。

- [ ] **Step 3: 实现**

在 `pkg/api/run_service.go`：

1. 加常量：

```go
const destroySyncTimeout = 2 * time.Minute
```

2. 加 `waitForCompletion`：

```go
// waitForCompletion blocks until run reaches a terminal status or the timeout
// elapses. Destroy uses it so that exclusive address-space resources (docker
// networks) are reclaimed before the destroy call returns — otherwise a rebuild
// that re-applies the same CIDR races the still-running destroy and fails with
// "Pool overlaps".
func (s *RunService) waitForCompletion(ctx context.Context, run *controlplane.Run) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(destroySyncTimeout)
	defer timeout.Stop()
	for {
		cur, ok := s.jobs.get(run.ID)
		if ok && cur.Status.IsTerminal() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("destroy %s did not complete within %s", run.ID, destroySyncTimeout)
		case <-ticker.C:
		}
	}
}
```

3. `startDestroy`（非幂等路径）在 `dispatchTopologyRun` 成功后加等待：

```go
	if err := s.dispatchTopologyRun(ctx, run, topology); err != nil {
		return nil, err
	}
	if err := s.waitForCompletion(ctx, run); err != nil {
		return nil, runError(runServiceInternal, err)
	}
	return run, nil
```

4. `startIdempotentDestroy` 在 `created` 且已 publish 之后加等待：

```go
	if created && s.scheduler.agents.registry != nil {
		_ = s.scheduler.agents.registry.PublishCommand(command.AgentID, command)
	}
	if created {
		if err := s.waitForCompletion(ctx, stored); err != nil {
			return nil, runError(runServiceInternal, err)
		}
	}
	return stored, nil
```

   （幂等重放 `found` 命中已存在 run 时 `created=false`，不等待。）

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `go test ./pkg/api/ -run TestStartDestroyWaitsForCompletion -v && go test ./pkg/api/... -count=1`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add pkg/api/run_service.go pkg/api/run_service_test.go
git commit -m "fix(api): destroy 同步等待资源回收，消除 rebuild 网络竞态"
```

---

## Self-Review

**Spec 覆盖：** #1→Task1、#2/#2c/#2d→Task2、#3→Task3、#4→Task4。全部覆盖。

**占位符扫描：** Task2 测试里 `substrate.docker.local` 的值语义、Task3 的 `Connection` 接口其余方法、Task4 的 RunService harness，都标了「实现前先确认」的注——这是**待核实项**而非占位符，实现者按注执行。

**类型一致性：** `SetWorkspaceDir(dir string)` 在 Task3 的 setter、测试、调用点一致；`waitForCompletion(ctx, *controlplane.Run) error` 在 Task4 的定义与两处调用一致。
