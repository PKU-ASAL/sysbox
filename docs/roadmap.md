# sysbox 开发路线图

> 版本：v0.1 · 2026-05-18
> 前置文档：[`refactor-plan-multi-substrate.md`](./refactor-plan-multi-substrate.md)（Wave 1 执行细节已归档于此，本文不重复）

---

## 1. 当前状态（Wave 1 已完成）

### 1.1 已完成

| 模块 | 状态 | 说明 |
|---|---|---|
| Substrate 接口 | ✅ | 19 个方法，全部定型 |
| BaseSubstrate | ✅ | 9 个安全默认实现 |
| Capabilities（11 字段） | ✅ | typed，覆盖 NICHotPlug / PIDVisibility 等 |
| NodeHandle / NodeSpec / ConnInfo / LinkRequest | ✅ | 全部 typed struct，无 `map[string]any` 漏洞 |
| Docker provider | ✅ | 7 个文件，完整实现接口 + 编译期 guard |
| Firecracker provider | ✅ | 8 个文件 + initbin，完整实现接口 |
| Runtime — 零 substrate 硬编码 | ✅ | `if subName == ...` 和具体类型断言全部清除 |
| State schema v2 | ✅ | SchemaVersion=2；v1 直接报错，不留兼容层 |
| HCL schema | ✅ | `provider "X" {}`、`connection {}`、`for_each`、`locals`、`output` |
| CLI 命令 | ✅ | 9 个命令（init/plan/apply/destroy/state/show/output/validate/sensor） |
| Examples（4 个） | ✅ | 全部有 `field.sysbox.hcl` + `lab.sh`，`make lab SUITE=xxx` 全部验证通过 |
| Makefile | ✅ | SUITE 参数化，12 个目标 |

### 1.2 进行中 / 未开始

| 模块 | 状态 | 说明 |
|---|---|---|
| Monitor Backend（Deploy/Collect/Remove） | ⚠️ | 当前仅 Start/Stop；`vm-vsock` 标注待替换 |
| edr-falcon backend | ❌ | 未开始 |
| libvirt substrate | ❌ | 未开始 |
| HTTP API 层（`pkg/api`） | ❌ | 未开始 |
| `sysbox serve` 命令 | ❌ | 未开始 |
| Terraform 差距（count / modules / import） | ❌ | 未开始 |

---

## 2. 架构概览（目标态）

```
┌───────────────────────────────────────────────────────────┐
│  入口层                                                     │
│  sysbox CLI (cobra)          sysbox serve (HTTP API)       │
│  make lab / make plan        GET/POST /v1/topologies/...   │
├───────────────────────────────────────────────────────────┤
│  核心引擎                                                   │
│  runtime.Executor      state.Manager     config.Parser      │
│  monitor.Backend       sensor.Event      sink.RoutingSink   │
├───────────────────────────────────────────────────────────┤
│  Substrate 适配层                                           │
│  docker.*   firecracker.*   libvirt.* (Wave 3)             │
└───────────────────────────────────────────────────────────┘
```

CLI 和 HTTP API 是平级关系，都是核心引擎的调用方，无包装关系。

---

## 3. Terraform 对齐差距分析

### 3.1 已对齐

| Terraform 概念 | sysbox 等价 |
|---|---|
| plan / apply / destroy | ✅ 完整实现 |
| state file + locking | ✅ flock + 原子写 |
| providers | ✅ substrate 接口（编译进二进制） |
| 9 种资源类型 | ✅ node/network/image/kernel/router/actor/monitor/ssh_access/firewall |
| outputs | ✅ |
| -target | ✅ `--target type.name` |
| depends_on | ✅ |
| state subcommands | ✅ list / show / mv / rm |
| for_each（部分） | ✅ loader.go 有 expandResource |
| locals / HCL 插值 | ✅ |
| --auto-approve | ✅ |
| validate | ✅ |
| drift detection（--refresh） | ✅ |

### 3.2 差距 · 按优先级排序

| 差距 | 优先级 | 理由 |
|---|---|---|
| `count` 元参数 | **P1** | 最常用的多实例语法，for_each 的整数简化版，1 天可完成 |
| `for_each` 完整（map/set 均支持） | **P1** | 已有骨架，完善边界条件 |
| `module` 块 | **P2** | 允许复用拓扑片段（共用 router 定义等）；Wave 3 |
| `data` source | **P2** | 查询已有 Docker 网络 / 已有 VM；Wave 3 |
| `import` 命令 | **P3** | 把已有容器/VM 纳入 state；研究工具暂不阻塞 |
| lifecycle 块（prevent_destroy 等） | **P3** | 防误删；目前靠 --auto-approve 约束 |
| remote state（S3/HTTP backend） | **P4** | 当前单机足够 |
| workspace 命名空间 | **P4** | SUITE= 参数已覆盖核心需求 |
| state schema 迁移工具 | **P3** | v1 → v2 目前直接报错让用户重 apply |

---

## 4. Wave 2 · EDR 重构 + HTTP API（~18 天）

> 目标：Monitor 从 Start/Stop 升级为 Deploy/Collect/Remove；同时新增 HTTP API 层。
> 两条线可并行开发。

### 4.1 Monitor 重构（PR-07/08，~7 天）

**PR-07 · Monitor Backend 接口重定义 + sysbox-collector（3 天）**

将 `monitor.Backend` 从 Start/Stop 流式接口改为 Deploy/Collect/Remove/Supports 四阶段模型：

```go
type Backend interface {
    Name() string
    Supports(t Target) bool

    // 把 agent 二进制投递到 guest，配置并启动（systemd unit / docker exec / vsock rpc）
    Deploy(ctx context.Context, sub substrate.Substrate, t Target, cfg AgentConfig) error

    // 启动事件中转，返回 normalised event channel
    Collect(ctx context.Context, t Target) (<-chan sensor.Event, error)

    // 卸载 agent（destroy 时调用）
    Remove(ctx context.Context, sub substrate.Substrate, t Target) error
}

type AgentConfig struct {
    BinaryURL   string
    SHA256      string
    BackendAddr string            // 指向 sysbox-collector 或外部 EDR
    Tags        map[string]string // episode_id, node_id, role...
    Extra       map[string]string
}
```

新增 `cmd/sysbox-collector`：接收 agent push 的 HTTP/JSON 事件流，落盘到 `runs/*/events/<node>.jsonl`。

```
guest agent ──POST /v1/events──> sysbox-collector ──> sink.RoutingSink ──> runs/*/events/
```

**PR-08 · edr-falcon backend（4 天）**

实现 `pkg/monitor/edr_falcon.go`：
- `Deploy`：通过 `substrate.Connection().CopyFile` 上传 agent 二进制 → `ExecInline` 安装为 systemd unit
- `Collect`：在 host 开 collector 端口，等 agent 反连 push
- `Remove`：`ExecInline` 停止 unit + 删文件
- 替换现有 `tracee` / `vm-vsock` backend，删除两个旧实现

删除 `pkg/monitor/tracee.go` 和 `pkg/monitor/vm_vsock.go`（已标注 TODO W2-PR-07）。

---

### 4.2 HTTP API 层（PR-09/10/11，~11 天）

**PR-09 · `sysbox serve` + 拓扑管理 API（4 天）**

新增 `pkg/api/` 包和 `cmd/sysbox/commands/serve_cmd.go`。

```
GET  /v1/health
GET  /v1/topologies                          扫描 runs/*/state.json，列出所有 suite
GET  /v1/topologies/{suite}/state            完整 state JSON
GET  /v1/topologies/{suite}/plan             计算 plan（只读，不执行）
POST /v1/topologies/{suite}/apply            触发异步 apply → {run_id}
POST /v1/topologies/{suite}/destroy          触发异步 destroy → {run_id}
GET  /v1/runs/{run_id}                       run 状态 + summary
GET  /v1/runs/{run_id}/logs                  SSE 流：apply/destroy 实时日志
```

异步 apply/destroy 实现：goroutine + in-memory job store。`runtime.Executor` 的 log 输出接入 SSE broadcast buffer。

认证：`SYSBOX_API_TOKEN` 环境变量，设置时要求 `Authorization: Bearer <token>` header，未设置则 dev 模式（无认证）。

包结构：
```
pkg/api/
  server.go           http.Server，middleware（auth/logging）
  routes.go           chi 路由注册
  handler_topo.go     plan/apply/destroy handlers
  jobs.go             Run store（in-memory）
  sse.go              SSE broadcast（log + events）
```

**PR-10 · 节点访问 API（4 天）**

```
GET  /v1/topologies/{suite}/nodes              从 state 列出节点
GET  /v1/topologies/{suite}/nodes/{name}       节点详情（handle + status）
POST /v1/topologies/{suite}/nodes/{name}/exec  执行命令，chunked streaming stdout/stderr
```

exec 实现：从 state 重建 NodeHandle → `substrate.Get(provider).Connection(handle).ExecInline(cmd)` → 输出写入 `http.ResponseWriter`（chunked）。

**PR-11 · 事件观测 API（3 天）**

```
GET  /v1/topologies/{suite}/events             SSE 流：所有 sensor events
GET  /v1/topologies/{suite}/events/{node}      SSE 流：单节点 events
```

实现：`sse.Broker` 订阅 `monitor.Backend.Collect()` 返回的 event channel，fan-out 给多个 SSE client。

```go
type Broker struct {
    mu   sync.RWMutex
    subs map[string][]chan sensor.Event  // suite → subscribers
}
```

---

### 4.3 Wave 2 PR 总览

| PR | 标题 | 估时 | 依赖 |
|---|---|---|---|
| PR-07 | Monitor Backend 重定义 + sysbox-collector | 3 天 | — |
| PR-08 | edr-falcon backend；删除 tracee + vm-vsock | 4 天 | PR-07 |
| PR-09 | `sysbox serve` + 拓扑管理 API（plan/apply/destroy/logs SSE） | 4 天 | — |
| PR-10 | 节点访问 API（exec streaming） | 4 天 | PR-09 |
| PR-11 | 事件观测 API（event SSE broker） | 3 天 | PR-09, PR-07 |

**Wave 2 总人天：~18 天**

PR-07/08（Monitor）和 PR-09/10（API）可并行开发，汇合点是 PR-11（event SSE 需要两条线都完成）。

---

## 5. Wave 3 · libvirt + Terraform 对齐（~22 天）

### 5.1 libvirt substrate（PR-12/13，~10 天）

**PR-12 · `pkg/provider/libvirt` 基础实现（7 天）**

- domain XML 生成（libvirt Go SDK）
- cloudinit NoCloud 镜像注入（PR-09 的 ImageSpec union 先完成）
- `CreateNode / StartNode / StopNode / DestroyNode / NodeStatus`
- `Connection`：SSH（同 FC），serial console
- `AttachNIC`：virsh attach-device + macvtap / bridge
- 新 example `examples/libvirt-vm/lab.sh`

**PR-13 · ImageSpec union（qcow2 / ISO / cloudinit）（3 天）**

```go
type ImageSpec struct {
    Kind      ImageKind          // docker | rootfs-ext4 | qcow2 | iso
    Source    string
    SHA256    string
    Size      string
    Cloudinit *CloudinitSeed
}
```

扩展 artifact resolver 支持 qcow2 下载 + sha256 验证。

### 5.2 Terraform 对齐（PR-14/15，~8 天）

**PR-14 · `count` 元参数 + `for_each` 完整化（3 天）**

```hcl
resource "sysbox_node" "attacker" {
  count = 3
  name  = "attacker-${count.index}"
  ...
}
```

`count.index` 注入 eval context；`for_each` 补全 set 类型支持和边界情况。

**PR-15 · `module` 块（5 天）**

```hcl
module "lab_network" {
  source = "./modules/three-tier-net"
  cidr_dmz      = "10.0.1.0/24"
  cidr_internal = "10.0.2.0/24"
}
```

递归解析 HCL + module 变量传递 + 模块内 output 引用。

### 5.3 三 substrate e2e（PR-16，~4 天）

`tests/e2e/multi_substrate_test.go`：docker + firecracker + libvirt 各一个节点，edr-falcon agent 全部接入同一个 sysbox-collector，事件流入 `runs/*/events/`。

### 5.4 Wave 3 PR 总览

| PR | 标题 | 估时 | 依赖 |
|---|---|---|---|
| PR-12 | libvirt substrate | 7 天 | — |
| PR-13 | ImageSpec union（qcow2/ISO/cloudinit） | 3 天 | — |
| PR-14 | count + for_each 完整化 | 3 天 | — |
| PR-15 | module 块 | 5 天 | — |
| PR-16 | 三 substrate + EDR e2e | 4 天 | PR-12, PR-08 |

**Wave 3 总人天：~22 天**

---

## 6. Wave 4 · 远期 backlog（不排期）

| 功能 | 触发条件 |
|---|---|
| `data` source（查询已有 Docker 网络 / VM） | 出现具体需求时 |
| `import` 命令（把已有容器/VM 纳入 state） | 出现迁移场景时 |
| `lifecycle` 块（prevent_destroy / ignore_changes） | 多人协作或 CI 保护需求 |
| remote state（S3 / HTTP backend） | 多机部署需求 |
| Episode 重置加速（Substrate.Pause / Resume） | 靶场高频重置场景 |
| Windows substrate（WinRM + sysprep + libvirt Windows guest） | Windows 靶场需求 |
| GPU passthrough / SR-IOV | 高级 VM 场景 |

---

## 7. 里程碑时间线

```
Wave 1 · 接口收口          ██████████████████████  完成（2026-05-18）
                            M1: 抽象稳定，双 substrate 可用

Wave 2 · EDR + API         ░░░░░░░░░░░░░░░░░░░░░░  ~18 天
 PR-07/08  Monitor          ████████████
 PR-09/10  API 拓扑+节点    ████████████
 PR-11     API 事件                      ████████
                            M2: HTTP API 可用；edr-falcon 接管监控

Wave 3 · libvirt + TF 对齐 ░░░░░░░░░░░░░░░░░░░░░░  ~22 天
 PR-12     libvirt          ████████████████████
 PR-13     ImageSpec        ████████
 PR-14     count/for_each   ████████
 PR-15     module           ████████████
 PR-16     e2e                               █████
                            M3: 三 substrate + count/module + API 完整
```

按 1 人 60% allocation：
- M2：约 5 周
- M3：约 10 周（M2 完成后继续）

---

## 8. 近期可立即启动（Wave 2 入口）

两条线可同时开：

**线 A（Monitor 重构）**：
1. 在 `pkg/monitor/monitor.go` 定义新 `Backend` 接口（Deploy/Collect/Remove/Supports）
2. 新建 `cmd/sysbox-collector/main.go`（HTTP event receiver）
3. 实现 `edr_falcon.go`，替换 tracee + vm-vsock

**线 B（API）**：
1. 新建 `pkg/api/server.go`（最小 chi server + health）
2. 新建 `cmd/sysbox/commands/serve_cmd.go`
3. 逐步添加 handler（先做 plan/state，再做 apply async，最后 exec 和 events）

两线汇合点：PR-11 event SSE，需要线 A 的 `Collect()` 接口和线 B 的 SSE broker 都就绪。
