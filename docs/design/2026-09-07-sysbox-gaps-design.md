# sysbox 六项缺口修复 —— 设计

日期：2026-09-07
状态：已评审，待写实施计划

## 一、背景

cyberfield 接入 sysbox 出多节点赛题时，反馈了 4 个 sysbox 侧问题（见
`cyberfield/docs/design/sysbox-gaps-and-sync.md`）。复盘后按「触类旁通」又发现 2
个同源问题（locals / count 的求值上下文缺 `substrate`）。本文覆盖全部 6 项。

## 二、问题与根因

| # | 问题 | 根因 | 正确性 / 体验 |
|---|---|---|---|
| 1 | `sysbox_firewall` 应用失败 | agent 没配 `pid: host`，看不到宿主机目标容器 PID | 正确性（网络分段不可用） |
| 2 | `locals` 不能引用 `var.*` / `local.*` | `localCtx` 只有 `Functions`，无 Variables | 体验 |
| 2c | `locals` 不能引用 `substrate.*` | 同上 | 体验 |
| 2d | `count`/`for_each` 不能引用 `substrate.*` | `preCtx` 有 local+var，无 substrate | 体验 |
| 3 | `provisioner "file"` 相对 source 不解析 | agent 没 chdir 到 workspace，相对路径落在 CWD | 正确性（装配脚本读不到） |
| 4 | rebuild 异步 destroy 竞态（`Pool overlaps`） | destroy 只派发 run、不等资源回收就返回 | 正确性（rebuild 卡死） |

## 三、设计

### #1 firewall —— agent 加 `pid: host` + caps

`deploy/docker/compose.agent.yml` 的 `sysbox-agent` 服务加：

```yaml
    pid: host
    cap_add:
      - NET_ADMIN
      - SYS_ADMIN
```

`NET_ADMIN`+`SYS_ADMIN` 是 `setns` 进目标 netns + 操作 nftables 的标准最小集。验证
redeploy 后 `docker inspect` 的 `PidMode=host`，且 `withContainerNetNS` 能 open
`/proc/<pid>/ns/net`。若 setns 仍 EPERM 再加 `privileged`（先测再定，不盲加）。

### #2 + #2c + #2d —— 统一 locals / count / for_each 求值上下文

`pkg/config/eval.go` 的 `buildEvalContextInner`：

- `localCtx`（locals 求值）：注入 `var`（`varBindings`）+ `substrate`（顶部已算好的
  `substrateVal`），并按**源码顺序**（按 `attr.NameRange` 排序）逐个求值 locals，边
  求边把已求出的 local 加入 `local` 对象 → 支持 `var.*`、先声明的 `local.*`、
  `substrate.*`。
- `preCtx`（count/for_each 求值）：补 `substrate`。

**边界**（对齐 Terraform）：locals/count 可引用 `var` + 先声明的 `local` +
`substrate`；**不**暴露资源运行时属性（仍只给 `id`/`name` 字符串桩）。前向 local
引用/循环不支持（与 Terraform「自上而下」语义一致；cty unknown 贯穿已单独延后）。

### #3 —— provisioner `file` 相对 source 相对 workspace 解析

- `pkg/runtime/executor.go`：加 `workspaceDir string` 字段 + `SetWorkspaceDir(dir)`。
- `pkg/runtime/resource_node.go` `runProvisioners` 的 `file` 分支：`expandTilde` 后，
  若 `src` 非绝对路径则 `filepath.Join(e.workspaceDir, src)`。
- `pkg/agentexec/executor.go` 三处 `SetRunContext` 旁，注入
  `filepath.Dir(e.bridge.HCLFile(run.Topology))`（= `workspacesDir/<topology>/`）。

### #4 —— destroy 同步回收

**plain destroy**（`POST /destroy` 无 `Idempotency-Key`，即 cyberfield rebuild 走的路径）
变同步：派发 run 后等待其到终态（轮询 run 状态，~250ms 间隔）再返回，网络连同所有资源
在返回前已回收。

- 落点 `pkg/api/run_service.go`：`startDestroy` 在 `dispatchTopologyRun` 后调
  `waitForCompletion`。
- **idempotent destroy**（带 `Idempotency-Key`）**保持异步**：其契约是「去重入队 +
  安全重放」，非「等待」；现有测试锁定该异步语义。若 cyberfield 将来用幂等键做 rebuild
  destroy，需另行同步（当前未用）。
- 超时 `destroySyncTimeout`（默认 120s），超时返回错误（让 rebuild **响亮失败**，
  不静默继续触发竞态）。等待期间 SSE 照常流式上报。
- 尊重 `r.Context()` 取消（HTTP 断开即停止等待）。

## 四、不做的事

- 不改 eval context 的整体结构（只在 `buildEvalContextInner` 内补缺）。
- 不做 cty.Value unknown 贯穿 / 变量类型系统补全（已单独延后）。
- 不加 `privileged`（测出来需要再加）。
