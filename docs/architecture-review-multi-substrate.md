# sysbox 多 Substrate 框架架构评审

> 目标：把 sysbox 从"容器 + Firecracker microVM"演进为**统一支持容器 / microVM / 重 VM（libvirt-QEMU、Cloud-Hypervisor）/ Windows VM** 的实验场框架。
>
> 评审范围：`pkg/substrate`、`pkg/provider/{docker,firecracker,exec,network}`、`pkg/runtime`、`pkg/monitor`、`pkg/config`。
>
> 评审时间：2026-05-16

---

## 0. 一句话结论

> **骨架是对的，但实现里 substrate-name 硬编码和 "NodeSpec 大杂烩" 已经在蔓延。现在跑 container + microVM 还撑得住，加第三种 substrate 一定会再写一轮 if/else 蔓延 —— 在引入重 VM 之前应当先收紧抽象。**

---

## 1. 现状打分

| 维度 | 分数 | 备注 |
|---|---|---|
| 接口骨架（Substrate / Connection / Monitor.Backend 三大抽象） | **A-** | 方向对、动词齐，但有 8 处明确瑕疵（见 §3） |
| Container 支持 | **A** | docker substrate 成熟，drift / cold-destroy 走通 |
| microVM 支持 | **B+** | firecracker substrate 跑通，但 vm-vsock sensor 仍是 stub，in-guest sensor 未 bundle |
| 抽象一致性（runtime 与 substrate 边界清晰度） | **C** | `runtime/resource_node.go` 大量 `if subName == "firecracker"`，NodeSpec 已被污染 |
| 重 VM（libvirt / QEMU / Cloud-Hypervisor）可扩展性 | **D** | 没有 placeholder、没有 cloud-init / qcow2 / ISO、没有 hot-plug 模型、没有 console 抽象 |
| Windows VM 可扩展性 | **D-** | Connection 只有 docker/ssh/vsock，缺 WinRM；Image 缺 answer file / sysprep |

---

## 2. 现状里做对的部分（保留）

| 设计 | 价值 |
|---|---|
| `Substrate` 接口动词集（PrepareImage / Create / Start / Stop / Destroy / Exec / Copy / AttachNIC / AttachTTY / ObservationHook / NodeStatus / Capabilities） | 覆盖了 guest-agnostic 的最小完整动作集 |
| `Capabilities{SharedKernel, SupportsWindows, BootTime, NICType}` | 给 VM/Windows 预留了元数据位（虽然现在字段太薄） |
| `sysbox_image` + `sysbox_kernel` 两类 artifact 分离 | 镜像和内核解耦，VM 用的 vmlinux 已经独立成一等公民 |
| `pkg/provider/exec/Connection`（docker-exec / ssh / vsock，`type=auto`） | provisioner 通道**完全脱离** substrate 类型，这是支持多 substrate 的关键解耦 |
| `monitor.Backend` 注册表 + `Target.Substrate` 字段 | 监控后端按 substrate 维度并存（tracee 走 docker，vm-vsock 走 fc） |
| `bridge + linux-netns` 作为统一网络底座 | veth 和 tap 都挂在同一个 bridge，跨 substrate 路由天然成立；NAT/firewall/router 复用同一套 nftables |
| `state` 模块（原子写 + 锁 + cold-destroy 路径） | substrate-neutral，崩溃恢复友好 |
| HCL `substrate "X" { alias = ... }` + per-resource `substrate = ...` 引用 | 让多 substrate 在同一 topology 共存有了语法位 |

---

## 3. 接口层的 8 处具体瑕疵（A- → A 要补的）

### 3.1 `ExecInNode` 与 `Connection.ExecInline` 双轨并存

`substrate.Substrate.ExecInNode` 与 `provider/exec.Connection.ExecInline` 含义重叠。runtime 里 provisioner 走 Connection，sensor 里某些路径走 substrate 自己的 exec。新 substrate 实现者会困惑该实现哪边。

**建议**：`Substrate` 不再暴露 exec，让 `Connection`（由 substrate 通过工厂方法返回）成为唯一 exec 入口。

```go
type Substrate interface {
    ...
    Connection(handle NodeHandle, hint ConnectionHint) (Connection, error)
}
```

### 3.2 `ObservationHook` 定义了但没人用

```go
type ObservationTarget struct { Kind string; Value string } // "host-pid-namespace" | "virtio-serial"
```

`tracee.go` 实际 `docker inspect | grep mntns`，完全绕开这个接口 → 接口和实现脱节。monitor backend 应该消费 `ObservationTarget`，而不是自己识别 substrate。

### 3.3 `Capabilities` 字段太薄

当前只有 `SharedKernel / SupportsWindows / BootTime / NICType`。无法表达：
- NIC 是否支持 hot-plug
- Disk 是否支持 hot-plug
- Console 类型（tty / serial / spice / vnc / none）
- 是否需要 cloud-init seed
- 是否支持暂停 / 快照 / 迁移
- PID 是否对 host 透出

→ runtime 没办法做 capability-driven dispatch，只能靠 `if subName == "firecracker"` 兜底。

### 3.4 `AttachNIC(handle, NIC)` 的 NIC 是 runtime 创建好的成品

runtime 在 `wireLink` 里就建好了 veth/tap，substrate 只是接受成品并 plug 进去。这违反"职责下沉" —— substrate 失去对设备形态的最终决定权（macvtap / SR-IOV VF / virtio-net-pci 透传 / vhost-user）。

**正确边界**：runtime 只交付**意图** `LinkRequest{bridge, netns, ip, gateway, idx}`，substrate 自己决定建什么。

### 3.5 `DockerCapable` 这种 type-assert 后门

```go
type DockerCapable interface { ConnectContainerToNetwork(...); GetContainerIP(...); ... }
```

一次性可接受，但意味着"把 node 接入既有网络"这种**通用动词没有进公共接口**。libvirt 进来时一定会重复出现 `LibvirtCapable`、`SpiceConsoleCapable`，每 substrate 一个 XxxCapable，runtime 就被锁死。

### 3.6 `ImageSpec` 只有 `DockerRef` / `Rootfs` / `Size`

重 VM 实际需要：cloud-image qcow2、cloud-init seed ISO、virt-install template、Windows ISO + autounattend.xml。当前 `ImageSpec` 没有位置，要么硬塞 Rootfs，要么往里加字段（继续污染）。

### 3.7 没有 plan-time validation hook

`Substrate` 接口里没有 `Validate(NodeSpec) error`，substrate 没机会在 plan 阶段说"我不支持 SharedKernel + Windows"或"我需要 KVM 但 /dev/kvm 不存在"。错误只能 apply 时炸。

### 3.8 `NodeHandle.Attributes map[string]any` 完全 untyped

`vsock_uds` / `vsock_cid` / `vsock_port` / `ssh_ip` / `vm_dir` / `network_netns` / `network_bridge` 全靠字符串 key 约定，runtime 里 `handle.Attributes["vsock_uds"].(string)` 一片，编译期没保护、IDE 没补全。

---

## 4. runtime / NodeSpec 层已经在蔓延的"裂缝"

### 4.1 `runtime/resource_node.go` 满地 `if subName == "firecracker"`

```go
// 1. 启动时序
if subName != "firecracker" { sub.StartNode(...) }  // Docker hot-plug
// ...AttachNIC loop...
sub.StartNode(...)                                  // Firecracker cold-plug

// 2. bridge name 注入
if subName == "firecracker" { handleWithSrc.Attributes["network_bridge"] = brName }

// 3. vsock 元数据持久化
if subName == "firecracker" { nodeInstance["vsock_uds"] = ... }

// 4. SSH IP 推断
if subName == "firecracker" && len(cfg.Links) > 0 { handle.Attributes["ssh_ip"] = firstIP }

// 5. wireLink 里 tap vs veth
if subName == "firecracker" { CreateTapInNetns(...) } else { CreateVethPair(...) }
```

加 libvirt 时这些 `if` 都会再分裂一次：5 个 → 10 个 → 15 个。

### 4.2 `NodeSpec` 是大杂烩

```go
type NodeSpec struct {
    // 通用
    Name, VCPUs, Memory, Env
    // Docker-only
    Privileged, PidMode, CgroupnsMode, Binds, InitialDockerNets   // ← 类型名直接叫 Docker
    // Firecracker-only
    Kernel, Rootfs, ChainInit
    // VM 视角
    SSHUser, SSHPass, SSHPort
}
```

每加一个 substrate 都要往公共结构里塞自家字段，越来越胖。

### 4.3 Monitor 的"路由"靠 backend 自己 reject

`tracee.Start` 里 `if tgt.Substrate != "docker" { skip }`，`vm-vsock.Start` 里 `if tgt.Substrate == "docker" { skip }`。三个 backend × 三个 substrate = O(N²) 拒绝判断。

### 4.4 Connection 抽象只有 4 种、缺 Windows 形态

Windows VM 没 vsock、SSH 也不一定有 → `WinRMConnection` 没位置。Connection 接口本身（`ExecInline/ExecBackground/CopyFile`）够通用，缺的是承认"connection 类型可能不止 4 个"。

### 4.5 没有重 VM substrate 的 placeholder

抽象的"对不对得起 VM"只有真写一个 stub 才知道。当前只有 2 个 substrate，所有 `if subName == "firecracker"` 看上去都合理 —— 直到加第三个。

---

## 5. 优化路线图

按"收益 / 风险"排序，分三波推进。

### Wave 1 · 抽象收紧（先于引入重 VM）

| 编号 | 改动 | 收益 | 估算 |
|---|---|---|---|
| W1-A | 拆 `NodeSpec`：通用字段 + `ProviderConfig`（typed-per-substrate decoder） | 解决 §4.2，消除所有 `*-only` 字段污染 | 中 |
| W1-B | 扩 `Capabilities`：加 `NICHotPlug / DiskHotPlug / ConsoleKind / NeedsCloudinit / PIDVisibility / SupportsPause` 等字段；runtime 用 caps 决定生命周期顺序 | 解决 §4.1（启动时序硬编码）和 §3.3 | 中 |
| W1-C | NIC 创建下沉到 substrate：runtime 只交付 `LinkRequest{bridge, netns, ip, gw, idx}`，`AttachNIC` 自己建 veth/tap | 解决 §3.4 + §4.1（wireLink 分支） | 中 |
| W1-D | `NodeHandle` 加 typed view：`type NodeHandle struct { ID string; Net NetInfo; Conn ConnInfo; Provider map[string]any }` | 解决 §3.8，提升类型安全 | 小 |
| W1-E | Monitor 加路由层：`Backend.Supports(Target) bool` + `monitor.RouteFor(target)`；一个 `sysbox_monitor` 横跨多 substrate | 解决 §4.3，未来加 backend 不修 runtime | 小 |
| W1-F | 真正用上 `ObservationHook`：substrate 自己回答"怎么观测"，tracee/vm-vsock 消费 `ObservationTarget` | 解决 §3.2，监控代码不再 docker-inspect | 小 |
| W1-G | `Substrate` 增加 `Validate(NodeSpec) error`，在 plan 阶段调用 | 解决 §3.7，apply 期错误前移 | 小 |
| W1-H | 合并 exec 路径：`Substrate.Connection(handle, hint) (Connection, error)`；移除 `ExecInNode/CopyToNode/CopyFromNode/AttachTTY` 中与 Connection 重叠的动词 | 解决 §3.1，新 substrate 实现者不再困惑 | 中 |

完成 Wave 1 后骨架可达 A，可以安心放重 VM 进来。

### Wave 2 · 引入重 VM（libvirt-QEMU）

| 编号 | 改动 | 备注 |
|---|---|---|
| W2-A | 扩 `ImageSpec` 为 union：`Kind ∈ {docker, rootfs-ext4, qcow2, iso}` + 可选 `Cloudinit CloudinitSeed` + 可选 `Sysprep WindowsAnswerFile` | 解决 §3.6 |
| W2-B | 加 `pkg/provider/libvirt`（或 `qemu`）substrate；先做 stub（只实现 Capabilities + Validate + 走通 plan），再逐步填 Create/Start/AttachNIC | 这是验证 Wave 1 抽象是否真的够用的"试金石" |
| W2-C | 加 `CloudinitProvisioner`：把 user-data / meta-data / network-config 打成 seed ISO 或走 NoCloud datasource | 重 VM 的标配 |
| W2-D | Console 抽象：`AttachTTY` 拓展为 `AttachConsole(kind ConsoleKind)`，支持 serial / spice / vnc | 调试 VM 必需 |
| W2-E | sensor：把 vm-vsock backend 通用化为 "in-guest agent backend"，QEMU 走 virtio-vsock（vhost-vsock）或 virtio-serial；落地一个共用的 in-guest sensor 二进制（基于 libbpf/CO-RE） | 关键的"VM 内行为采集"能力 |

### Wave 3 · Windows + 高级特性

| 编号 | 改动 | 备注 |
|---|---|---|
| W3-A | 加 `WinRMConnection`（或 `OpenSSHWindowsConnection`） | 解决 §4.4 |
| W3-B | sensor：Windows 走 ETW provider（用 `Microsoft-Windows-Kernel-Process` 等）；新增 `etw` backend | 对应 `Capabilities.SupportsWindows` |
| W3-C | 快照 / 暂停 / migrate：`Substrate.Pause/Resume/Snapshot/Restore`；用于"重置 episode" | 训练/回放场景需要 |
| W3-D | GPU passthrough / SR-IOV VF：`AttachDevice` 接口，覆盖 VFIO 场景 | 高级特性，可选 |

---

## 6. 重构后的目标接口形态（示意）

```go
// pkg/substrate/substrate.go

type Substrate interface {
    Name() string
    Capabilities() Capabilities
    Validate(spec NodeSpec) error                       // plan-time hook

    PrepareImage(ctx, ImageSpec) (ImageRef, error)
    CreateNode(ctx, NodeSpec) (NodeHandle, error)
    StartNode(ctx, NodeHandle) error
    StopNode(ctx, NodeHandle) error
    DestroyNode(ctx, NodeHandle) error
    NodeStatus(ctx, NodeHandle) (Health, error)

    AttachNIC(ctx, NodeHandle, LinkRequest) (NIC, error) // intent-based
    DetachNIC(ctx, NodeHandle, nicID string) error

    Connection(NodeHandle, ConnectionHint) (Connection, error) // 唯一 exec 入口
    Console(NodeHandle, ConsoleKind) (io.ReadWriteCloser, error)

    ObservationHook(ctx, NodeHandle) (ObservationTarget, error)
}

type Capabilities struct {
    SharedKernel    bool
    SupportsWindows bool
    NICHotPlug      bool
    DiskHotPlug     bool
    NICKinds        []string  // ["veth"] | ["tap","macvtap","vfio"]
    ConsoleKinds    []string  // ["tty","serial","spice"]
    NeedsCloudinit  bool
    PIDVisibility   PIDMode   // "host" | "ns" | "opaque"
    SupportsPause   bool
    SupportsSnapshot bool
    BootTime        Duration  // 显式 typed duration，不再 "ms" / "seconds" 字符串
}

type NodeSpec struct {
    Name      string
    Resources Resources             // VCPUs / Memory / Disk
    Env       map[string]string
    Links     []LinkRequest         // 意图，不是成品
    Connection ConnectionHint
    Image     ImageRef
    Provider  ProviderConfig        // typed union: docker.Config | firecracker.Config | libvirt.Config
}

type NodeHandle struct {
    ID       string
    Net      NetInfo                // typed: { Netns, Bridge, PrimaryIP }
    Conn     ConnInfo               // typed: { Kind, Endpoint, Auth }
    Provider any                    // substrate-specific, 但只在该 substrate 内部 type-assert
}

type LinkRequest struct {
    Network  string  // bridge name
    Netns    string  // netns name (host-managed)
    IP       string  // CIDR
    Gateway  string
    Idx      int
    Kind     NICKind // optional hint: veth/tap/macvtap/vfio
}

type ImageSpec struct {
    Kind      ImageKind             // "docker" | "rootfs-ext4" | "qcow2" | "iso"
    Source    string                // URL or local path
    SHA256    string
    Size      string
    Cloudinit *CloudinitSeed        // 可选
    Sysprep   *WindowsAnswerFile    // 可选
}
```

---

## 7. 风险与权衡

| 风险 | 缓解 |
|---|---|
| Wave 1 是接口级 break change，会同时改 docker + firecracker 两个 provider | 一次性合并，配 `tests/e2e` 跑全量；接口冻结期 ≤ 1 周 |
| `ProviderConfig` typed union 在 Go 里没有原生 sum type → 用 `any` + 各 substrate 自己 decode | 加 `Substrate.DecodeProviderConfig(hcl.Body) (any, error)` 工厂方法；运行期类型安全靠 substrate 自己 type-assert，**只在 substrate 包内部** |
| 重构期间 examples / playbooks 可能跑不通 | 给每个 example 加 `// substrate=docker` / `// substrate=firecracker` 标记，CI 矩阵分别验证 |
| W2-B 写 libvirt stub 的 ROI 在没真要跑前不明显 | 不要求功能完整，目标只是"plan 走通 + Validate 报合理错"，纯属抽象验证；预算 ≤ 1 天 |
| 接口扩字段会让旧 substrate 长期保留"默认实现" | 在 `pkg/substrate` 提供 `BaseSubstrate` 嵌入，给出最小默认；substrate 自己覆写需要的方法 |

---

## 8. 行动建议（给评审者的最短路径）

1. **决定 Wave 1 的范围**：是 A 全做还是只挑 A/B/C/E 四项最痛的？
2. **决定 `ProviderConfig` 形式**：`map[string]any` vs 每 substrate 一个 typed Config + 工厂 decoder（推荐后者）
3. **决定是否引入 libvirt stub 作为抽象试金石**（强烈推荐：是）
4. **决定 in-guest sensor 的统一形态**：基于 libbpf-CO-RE 自研 vs 移植 tracee 进 guest
5. 排定 Wave 1 时间窗（建议合并到一个 PR，避免中间态混乱），然后再按 Wave 2 / Wave 3 节奏推进

---

## 附录 A · 现有 `if subName == "firecracker"` 分支清点

| 文件 | 行为 | Wave 1 归属 |
|---|---|---|
| `pkg/runtime/resource_node.go::createNode` | StartNode 时序分歧 | W1-B (Capabilities-driven lifecycle) |
| `pkg/runtime/resource_node.go::createNode` | 注入 `network_bridge` 到 handle | W1-C (NIC 下沉) |
| `pkg/runtime/resource_node.go::createNode` | 持久化 vsock 元数据 | W1-D (typed handle) |
| `pkg/runtime/resource_node.go::createNode` | 从第一个 link IP 推断 ssh_ip | W1-D + Connection 工厂 |
| `pkg/runtime/resource_node.go::wireLink` | tap vs veth 分支 | W1-C |
| `pkg/runtime/resource_node.go::destroyNode` | tap vs veth 清理 | W1-C |
| `pkg/runtime/resource_node.go::connectionForNode` | substrate 名字 → connection 类型 | W1-H (Substrate.Connection 工厂) |
| `pkg/monitor/tracee.go::Start` | 跳过 non-docker target | W1-E (Backend.Supports) |
| `pkg/monitor/vm_vsock.go::Start` | 跳过 docker target | W1-E |

清掉这 9 处后，runtime 应当完全不再出现 substrate 字符串硬编码。

---

*评审人：Droid（架构 review pass）  · 日期：2026-05-16*
