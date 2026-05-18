# sysbox 多 Substrate 重构执行计划（系统性收口版）

> 配套文档：[`architecture-review-multi-substrate.md`](./architecture-review-multi-substrate.md)
>
> 本次重构定位：**系统性收口清理**，不保留兼容层。一切按"最合理的架构"重写；老 HCL、老 state、老接口方法**直接删除**。版本号同步跳 `v1.0`。
>
> In-guest sensor **不归 sysbox 管**：上层有 Falcon-like EDR 工具负责采集；sysbox 只负责**把 agent 注入到 guest** 和**把 agent 的事件流落到 `runs/*/events/`**。
>
> 制定时间：2026-05-16

---

## 0. 收口原则

| 原则 | 含义 |
|---|---|
| **No backward compat** | 不写 HCL 兼容层、不写 state v1→v2 迁移、不留 Deprecated 接口方法。要么改，要么删 |
| **Interface first** | 先把目标接口（Substrate / Connection / Monitor）一次性敲死，再 fork docker+fc provider 去对齐 |
| **One PR = one architectural decision** | 每个 PR 解决一个抽象层裂缝；不混"修 bug" |
| **EDR-agnostic monitor** | sysbox 不再"理解事件语义"，只负责 agent 注入 + 事件流转运 |
| **No in-guest sensor in sysbox** | guest 内的探针属于上层 EDR；sysbox 提供注入通道 + 采集 sink |

---

## 1. 目标接口（必读 · 一次性定稿）

### 1.1 Substrate

```go
// pkg/substrate/substrate.go

type Substrate interface {
    Name() string
    Capabilities() Capabilities
    Validate(spec NodeSpec) error                              // plan-time
    DecodeProviderConfig(body hcl.Body) (any, error)           // 每 substrate 自己 decode 自家 `provider {}` 块

    PrepareImage(ctx, ImageSpec) (ImageRef, error)
    CreateNode(ctx, NodeSpec) (NodeHandle, error)
    StartNode(ctx, NodeHandle) error
    StopNode(ctx, NodeHandle) error
    DestroyNode(ctx, NodeHandle) error
    NodeStatus(ctx, NodeHandle) (Health, error)

    AttachNIC(ctx, NodeHandle, LinkRequest) (NICResult, error) // intent-based
    DetachNIC(ctx, NodeHandle, nicID string) error

    Connection(NodeHandle, ConnectionHint) (Connection, error) // 唯一 exec 入口
    Console(NodeHandle, ConsoleKind) (io.ReadWriteCloser, error)

    ObservationHook(ctx, NodeHandle) (ObservationTarget, error)
}
```

> **删除**：旧版的 `ExecInNode / CopyToNode / CopyFromNode / AttachTTY` 全部清掉，由 `Connection` 承担。

### 1.2 Capabilities（11 字段，typed）

```go
type Capabilities struct {
    SharedKernel     bool
    SupportsWindows  bool
    NICHotPlug       bool
    DiskHotPlug      bool
    NICKinds         []string   // veth | tap | macvtap | vfio
    ConsoleKinds     []string   // tty | serial | spice | vnc
    NeedsCloudinit   bool
    PIDVisibility    PIDMode    // host | ns | opaque
    SupportsPause    bool
    BootTime         time.Duration
    Notes            string
}
```

### 1.3 NodeSpec / NodeHandle（typed，无 substrate-leak）

```go
type NodeSpec struct {
    Name       string
    Resources  Resources                  // VCPUs / Memory / Disk
    Image      ImageRef
    Env        map[string]string
    Links      []LinkRequest              // 意图，不是成品
    Connection ConnectionHint             // type=auto/ssh/vsock/winrm/docker
    Provider   any                        // typed: docker.Config | firecracker.Config | libvirt.Config
}

type NodeHandle struct {
    ID       string
    Net      NetInfo                      // typed: { Netns, Bridge, PrimaryIP }
    Conn     ConnInfo                     // typed: { Kind, Endpoint, Auth }
    Provider any                          // substrate-specific，只在 substrate 包内 type-assert
}

type LinkRequest struct {
    Network  string   // bridge name
    Netns    string   // host-managed netns
    IP       string   // CIDR
    Gateway  string
    Idx      int
    KindHint NICKind  // optional: veth/tap/macvtap/vfio
}
```

### 1.4 ImageSpec（union for container / VM / Windows）

```go
type ImageSpec struct {
    Kind      ImageKind                   // docker | rootfs-ext4 | qcow2 | iso
    Source    string                      // URL or local path
    SHA256    string
    Size      string
    Cloudinit *CloudinitSeed              // optional, for VM
    Sysprep   *WindowsAnswerFile          // optional, for Windows
}
```

### 1.5 Monitor（EDR-agnostic）

> **关键转变**：sysbox 不再自己解析 syscall。Monitor 模块退化成"**Agent 注入器 + 事件中转**"。

```go
// pkg/monitor/monitor.go

type Backend interface {
    Name() string

    // Deploy: 把 agent 投递到 target 节点（通过 substrate 的 Connection）。
    // 实现示例：scp/docker-cp/cloudinit 注入 + systemd unit 启动。
    Deploy(ctx context.Context, sub substrate.Substrate, t Target, cfg AgentConfig) error

    // Collect: 启动事件中转。两种实现路径：
    //   - "pull": 在 host 上拉 agent 的 socket/管道
    //   - "push": agent 主动连到 sysbox-collector，本方法只是登记 sink
    // 返回 normalised event channel；channel 关闭表示 collect 结束。
    Collect(ctx context.Context, t Target) (<-chan sensor.Event, error)

    // Remove: agent 卸载（可选；apply destroy 时调用）。
    Remove(ctx context.Context, sub substrate.Substrate, t Target) error

    Supports(t Target) bool   // 解决 §4.3 的 backend 自行 reject 问题
}

type AgentConfig struct {
    BinaryURL    string            // EDR agent 安装包（URL 或 local path）
    SHA256       string
    BackendAddr  string            // 上报地址（指向 sysbox-collector 或外部 EDR control plane）
    Tags         map[string]string // 标签（episode_id, node_id, role 等）
    Extra        map[string]string // backend-specific
}
```

---

## 2. 显式破坏清单（CHANGELOG 列在这里）

以下变更**直接破坏老用户**，在 v1.0 release notes 里集中说明：

### 2.1 HCL 破坏

| 老写法 | 新写法 |
|---|---|
| `privileged = true` (顶层) | `provider "docker" { privileged = true }` |
| `pid_mode / cgroupns_mode / binds` (顶层) | `provider "docker" { ... }` |
| `kernel / rootfs / chain_init` (顶层) | `provider "firecracker" { ... }` |
| `ssh_user / ssh_pass / ssh_port` (顶层) | `connection { type = "ssh"  user = ... }` |
| `vcpus / memory` (顶层) | `resources { vcpus = ... memory = ... }` |
| `sysbox_monitor { backend = "tracee" events = [...] }` | `sysbox_monitor { backend = "edr-falcon" agent { binary = ... } }` |

### 2.2 State 破坏

- `runs/*/state.json` schema 完全换；**不写迁移**
- 用户必须 `sysbox destroy --legacy` 清掉老 state（或手动 `rm -rf runs/`），再用 v1.0 重新 apply
- 提供 `sysbox state inspect` 工具读 v1，仅用于 debug，不做自动转换

### 2.3 Go API 破坏

- 删除 `Substrate.ExecInNode / CopyToNode / CopyFromNode / AttachTTY`
- 删除 `DockerCapable / VMCapable` 这类 type-assert 后门
- `NodeHandle.Attributes map[string]any` 整个删除，改 typed `NodeHandle.Net / Conn / Provider`
- `monitor.Target.Substrate string` 删除（路由靠 `Backend.Supports`）

---

## 3. PR 序列

### Wave 1 · 接口收口（6 个 PR，下调 2 个）

> 不再有 PR-08 (Deprecated 兼容)。所有"删除"动作直接做。

| PR | 标题 | 主要修改 | 估时 | 风险 |
|---|---|---|---|---|
| **PR-01** | 目标接口一次性定稿：`Capabilities` 扩字段 + `Validate` + `DecodeProviderConfig` + `BaseSubstrate` | `pkg/substrate/*`（破坏式重写） | 1 天 | 低（纯接口） |
| **PR-02** | typed `NodeHandle` + typed `NodeSpec.Provider` + state schema 直接换 v2（无迁移） | `pkg/substrate/types.go`, `pkg/state/*`, docker+fc provider 同 PR 更新 | 2 天 | 中 |
| **PR-03** | HCL schema 重写：`resources {} / provider "X" {} / connection {}` 三个嵌套块；同步改两个 examples | `pkg/config/schema.go`, `examples/three-nodes/`, `examples/microvm/` | 2 天 | 中 |
| **PR-04** | NIC 创建下沉：`LinkRequest` + 删除 runtime 的 `wireLink`；docker+fc 各自实现 `AttachNIC` | `pkg/substrate/types.go`, `pkg/provider/{docker,firecracker}/nic.go`, `pkg/runtime/resource_node.go` 大瘦身 | 2 天 | 中 |
| **PR-05** | Capabilities-driven 生命周期：runtime 删除所有 `if subName == "firecracker"`；启动时序按 `Capabilities.NICHotPlug` 决定 | `pkg/runtime/resource_node.go`（最终瘦身） | 1 天 | 低 |
| **PR-06** | `Substrate.Connection / Console` 工厂方法 + 删除 `ExecInNode/CopyToNode/CopyFromNode/AttachTTY`；runtime 与 monitor 全切到 Connection | `pkg/substrate/substrate.go`, `pkg/provider/*/conn.go`(新), `pkg/runtime/*`, `pkg/monitor/*` | 2 天 | 中 |

**Wave 1 完工后状态**：
- `runtime` 零 substrate 字符串硬编码
- `NodeSpec` 通用字段 ≤ 7 个（其余进 `Provider any`）
- 删除约 9 处 `if subName == ...`、3 处 `XxxCapable` type-assert
- 新加 substrate 只需要：实现 `Substrate` 接口 + 注册 + 自己的 `provider {}` decoder

**总人天**：~10 天

---

### Wave 2 · EDR 集成 + 重 VM（5 个 PR）

> in-guest sensor 由外部 Falcon-like EDR 提供；sysbox 这一波只做**注入 + 中转**。

| PR | 标题 | 主要修改 | 估时 | 风险 |
|---|---|---|---|---|
| **PR-07** | `monitor.Backend` 重定义（Deploy / Collect / Remove / Supports） + `sysbox-collector` HTTP/gRPC 入口（接 agent push） | `pkg/monitor/*`（重写）, `cmd/sysbox-collector/`(新) | 3 天 | 中 |
| **PR-08** | **`edr-falcon` backend**：通过 `Connection` 把 agent 二进制投递到 guest，写 systemd unit / Windows service，注册到 collector | `pkg/monitor/edr_falcon.go`(新), 配合 docker+fc+libvirt | 4 天 | 中 |
| **PR-09** | `ImageSpec` union（qcow2 / ISO / cloudinit seed / sysprep） + artifact resolver 扩展 | `pkg/substrate/types.go`, `pkg/artifact/*`, `pkg/config/schema.go` | 2 天 | 低 |
| **PR-10** | `pkg/provider/libvirt` 完整实现：domain XML + cloudinit NoCloud + virsh start/destroy + Console(spice/serial) | `pkg/provider/libvirt/*`(新), `examples/libvirt-vm/`(新) | 6 天 | 高 |
| **PR-11** | 三 substrate 跑 e2e：docker + firecracker + libvirt 各一个节点，EDR agent 全部接入同一个 collector，事件流入 `runs/<id>/events/<node>.jsonl` | `tests/e2e/multi_substrate_test.go`(新) | 2 天 | 中 |

**Wave 2 完工后状态**：
- 三种 substrate 同时可用（container + microVM + VM）
- EDR agent 注入路径统一（通过 `Substrate.Connection`）
- 事件流统一进 `runs/*/events/`，不区分 substrate

**总人天**：~17 天

---

### Wave 3 · 远期计划（不在本次重构范围）

> 本次系统性收口完成 Wave 1+2 即收工。Wave 3 列为**远期 backlog**，等 M2 落地、实际需求出现再启动。
>
> 候选项（按可能优先级）：
> - **Episode 重置加速**：`Substrate.Pause / Resume`（libvirt: virsh suspend/resume; fc: SIGSTOP/SIGCONT; docker: pause/unpause）
> - **Windows 支持**：`WinRMConnection` + `ImageSpec.Sysprep` + libvirt Windows guest + EDR agent Windows 投递路径
> - GPU passthrough / SR-IOV、live-migrate、snapshot tree —— 目前没看到需求，暂不规划

---

## 4. 时间预算

| 阶段 | PR 数 | 人天 | 累计 | Milestone |
|---|---|---|---|---|
| Wave 1（接口收口） | 6 | 10 | 10 | **M1**：抽象稳定 |
| Wave 2（EDR 集成 + libvirt） | 5 | 17 | 27 | **M2**：三 substrate + EDR 全打通 |
| ~~Wave 3~~ | — | — | — | 远期 backlog，不在本轮 |

按 1 人 60% allocation 算：约 **6 周**走完 M2（含兼容版需 ~14 周）。

---

## 5. EDR 集成详细设计（PR-07/08）

### 5.1 数据通路

```
┌─────────────────────────────────┐                ┌──────────────────────┐
│  guest (container / VM)         │                │  host: sysbox        │
│                                 │                │                      │
│  ┌─────────────────────────┐   push events       │  ┌──────────────┐    │
│  │ EDR agent (falcon-like) │ ──────────────────► │  │ sysbox-      │    │
│  │  - process telemetry    │   gRPC/HTTP/UDS     │  │  collector   │    │
│  │  - file telemetry       │                     │  └──────┬───────┘    │
│  │  - network telemetry    │                     │         │            │
│  │  - module load          │                     │         ▼            │
│  │  - module load          │                     │  ┌──────────────┐    │
│  │  - auth/session         │                     │  │ Sink router  │    │
│  │  - script-based detect  │                     │  │ (per-node)   │    │
│  │  - in-memory artifacts  │                     │  └──────┬───────┘    │
│  └─────────────────────────┘                     │         ▼            │
│                                 │                │  runs/<id>/events/   │
└─────────────────────────────────┘                │     <node>.jsonl     │
                                                   └──────────────────────┘
```

### 5.2 sysbox 需要做的事（边界清晰）

| 职责 | sysbox 做 | EDR 做 |
|---|---|---|
| Agent 二进制分发 | ✓ 通过 `Connection.CopyFile` 投递到 guest | ✗ |
| Agent 启动 | ✓ 通过 `Connection.ExecInline` 安装为 systemd unit / Windows service | ✗ |
| Agent 卸载 | ✓ `Backend.Remove` 反向操作 | ✗ |
| 事件采集（syscall / file / net） | ✗ | ✓ EDR 全权 |
| 事件解码 / 归一化 | ✗ | ✓ EDR agent 输出统一 JSON |
| 事件传输 | ✓ 提供 collector endpoint，让 agent push | ✗ |
| 事件落盘 | ✓ 写 `runs/<id>/events/<node>.jsonl` | ✗ |
| IOA/IOC 检测 | ✗ | ✓ EDR / 上层应用 |

### 5.3 与 Falcon-like 能力对齐

| Falcon-like 能力 | sysbox 支持点 |
|---|---|
| Process telemetry (exec/fork/exit) | event passthrough；schema 由 EDR 定 |
| File telemetry (open/create/delete/rename) | 同上 |
| Network telemetry (connect/accept/dns) | 同上 |
| Module load telemetry | 同上 |
| Auth / session telemetry | 同上 |
| Registry telemetry (Windows) | Wave 3 PR-13 |
| Script-based detection | 不在 sysbox 范围（上层应用消费 events） |
| In-memory artifacts | 同上 |
| Real-Time Response (远程 shell) | sysbox 已经有 `sysbox_actor` + ACP 通道，不重复 |
| Tags / metadata enrichment | `AgentConfig.Tags` 注入 episode_id / node_id |

### 5.4 collector 接口

```go
// cmd/sysbox-collector

// POST /v1/events  (gRPC 同等接口)
// Header: Authorization: Bearer <per-node-token>
// Body:   newline-delimited JSON (one event per line)
//         {"ts": ..., "node_id": "...", "category": "...", "raw": {...}}
//
// Sysbox 验证 token → 取出 node_id → 追加到 runs/<run_id>/events/<node_id>.jsonl
```

token 在 `Backend.Deploy` 阶段生成、注入 agent 配置文件，destroy 时回收。

---

## 6. 决策清单（已敲定）

| # | 问题 | 决定 |
|---|---|---|
| 1 | Wave 1 一次合并还是按 PR 串行评审？ | 串行；每个 PR 独立 review，1-2 天合一个 |
| 2 | v1.0 发布前要不要给老 HCL 留个**只读** `sysbox migrate-hcl` 工具？ | **不留**。CHANGELOG 写清楚破坏点，用户手工改 HCL |
| 3 | EDR agent 二进制谁负责打包？sysbox 仓库直接 embed 还是 URL fetch？ | URL fetch + sha256，与 kernel artifact 一致 |
| 4 | collector 走 HTTP 还是 gRPC？ | HTTP/JSON；预留 gRPC 但 v1.0 不实现 |
| 5 | Wave 3 真要做 Windows 吗？时间窗？ | **不做**。Windows 进远期 backlog，本轮收工到 M2 |
| 6 | libvirt 还是直接 QEMU？ | libvirt（XML domain 标准 IR，virt-install / virsh 成熟） |
| 7 | 谁跑 e2e？（需要 Docker + root，PR-10 还需 libvirtd + KVM） | 本地 + 一台带 KVM 的开发机；CI 仅跑 unit + lint |

---

## 7. 立即可启动项（quick wins）

| 项 | 内容 | 用时 |
|---|---|---|
| **A1** | 起 v1 接口分支 `feat/v1-substrate`（不保留老接口共存；要么直接改要么删） | 0.2 天 |
| **A2** | `pkg/substrate/base.go` 加 `BaseSubstrate{}`，提供默认 `Validate / Console / ObservationHook`，方便后续 provider 嵌入 | 0.5 天 |
| **A3** | 给 9 处 `if subName == ...` 加 `// TODO(W1-PR-05): capability-driven`，重构时一眼定位 | 0.3 天 |
| **A4** | 起草 `CHANGELOG.v1.0.md`，把 §2 "显式破坏清单"原文搬过去 | 0.5 天 |

---

## 8. 关键差异 vs 旧 plan

| 维度 | 旧 plan（含兼容） | 新 plan（系统性收口 + Wave3 远期化） |
|---|---|---|
| Wave 1 PR 数 | 8（含 PR-08 Deprecated） | **6**（直接删除） |
| Wave 2 PR 数 | 6（含 in-guest sensor 自研） | **5**（in-guest sensor 外部 EDR 提供） |
| Wave 3 | 必做 | **远期 backlog** |
| 兼容层代码 | HCL parser 双 schema + state v1↔v2 迁移 + 接口 Deprecated 别名 + migrate-hcl 工具 | **零** |
| 总人天到 M2 | ~41 | **~27**（省 14 天） |
| Monitor 模块 | 复杂（自管 tracee + vm-sensor + ETW） | **极简**（agent 注入 + 事件中转） |
| 用户升级路径 | "升级即可" | "destroy 老 lab + 改 HCL + apply" |

---

*执行计划编制：Droid · 日期：2026-05-16 · 版本：v0.3 系统性收口 + 外部 EDR + Wave3 远期化*
