# Sysbox Overview

Sysbox 是一个面向 Linux 实验环境的声明式拓扑控制面。用户用 HCL 声明节点、网络、artifact、依赖关系和生命周期意图，Sysbox 负责计算计划、调用对应 provider、持久化状态，并在失败后从 checkpoint 恢复或清理。

它解决的核心问题不是“启动一个容器或虚拟机”，而是让一整张异构实验拓扑具有统一、确定且可审计的生命周期。

## 目的与范围

Sysbox 面向以下场景：

- 安全研究和攻防实验环境。
- 容器、microVM 和 VM 混合的系统实验。
- 需要固定网络身份和可重复执行的网络验证。
- 需要本地 CLI，或 API、Agent、Web 控制面的实验平台。
- 需要在每轮实验后从不可变 baseline 重建 guest 的工作流。

Sysbox 不是通用云资源编排器，也不兼容任意 Terraform provider 或 Terraform module。它刻意控制 provider 和资源范围，以换取更严格的状态、恢复和宿主机资源所有权边界。

## 拓扑模型

一份 HCL 配置会被解析为带类型的资源图。引用和显式依赖共同决定执行顺序；runtime 按依赖顺序创建资源，按逆依赖顺序执行破坏性操作。

主要资源包括：

| 资源 | 职责 |
|---|---|
| `sysbox_image` | 声明 OCI、rootfs 或 qcow2 artifact 及其身份 |
| `sysbox_kernel` | 声明 Firecracker 内核 artifact |
| `sysbox_network` | 声明隔离网络、CIDR 和 NAT 意图 |
| `sysbox_node` | 声明 Docker、Firecracker 或 libvirt guest |
| `sysbox_router` | 声明多接口转发节点 |
| `sysbox_firewall` | 声明挂载到网络的 nftables 策略 |
| `sysbox_ssh_access` | 声明 SSH 入口和 authorized key 注入 |
| `sysbox_actor` | 声明可选的 ACP 兼容 Agent 容器 |

资源使用稳定地址，例如 `sysbox_node.web`。地址语法和重命名的破坏性规则见 [Resource Addresses](architecture/resource-addresses.md)。

HCL 中的资源地址是逻辑身份，provider 创建的 container ID、domain UUID 或 Firecracker generation ID 是 external identity。逻辑身份可以在 reset 前后保持不变，而 external identity 必须随可变 guest 的替换而改变。

## 异构 Provider

Sysbox 当前正式支持三类节点 provider：

- **Docker**：适合启动快、密度高的容器实验节点。
- **Firecracker**：使用显式 kernel 和 rootfs artifact，提供 microVM 隔离。
- **libvirt**：使用 qcow2 baseline 和每代 overlay，适合完整 Linux VM。

Linux network provider 负责 netns、bridge、veth、TAP、地址和链路等宿主机网络原语。在 guest network initialization 和 reset orchestration 中，runtime 只依赖公共能力契约，不理解 NoCloud、netplan、qcow2 overlay 或 Firecracker socket 等具体机制；provider 在正确的生命周期阶段执行这些机制。其他资源处理和历史 checkpoint recovery 路径仍包含按 driver 能力或类型区分的逻辑，不能据此宣称整个 runtime 完全 provider-neutral。

接口边界和状态所有权见 [Handler and Driver Contracts](architecture/handler-driver-contracts.md)。完整的三 provider HCL 见 [heterogeneous-matrix](../examples/heterogeneous-matrix/field.sysbox.hcl)。

## Artifact 与 Guest 身份

`sysbox_image` 将 artifact 的来源和运行语义变成显式模型：

```hcl
resource "sysbox_image" "guest" {
  substrate   = substrate.libvirt.local
  kind         = "qcow2"
  source       = env("SYSBOX_QCOW2")
  architecture = "amd64"
  guest_family = "linux"
}
```

核心字段是：

- `kind`：`oci`、`rootfs` 或 `qcow2`。
- `source`：镜像引用或宿主机 artifact 路径。
- `architecture`：当前 artifact 的 CPU 架构。
- `guest_family`：provider 可以据此判断 guest 能力；当前验收使用 `linux`。
- `sha256`：可选的期望 digest，用于显式 pin。

plan 和 reset 使用解析后的不可变 identity，而不是把可变路径或 tag 当成永久身份。执行 reset 前会重新校验 baseline digest；baseline 已变化时拒绝继续。

大型 artifact 不进入仓库。Firecracker artifact 的准备和缓存约定见 [Firecracker Artifacts](firecracker-artifacts.md)。

## 网络与 Guest 初始化

Sysbox 的正式拓扑网络目前以 IPv4 为主。HCL link 声明逻辑网络、固定 IPv4 prefix，并可携带稳定 MAC；network provider 将它们实现为 bridge、netns、veth 或 TAP 连接。

宿主机链路创建与 guest 内部网络配置是两个不同阶段：

1. runtime 根据统一 attachment 模型完成宿主机 NIC wiring。
2. runtime 检查 provider 声明的 `GuestNetworkInitMode` 能力。
3. provider 在自己的生命周期阶段执行 `cloud_init` 或 `preconfigured` 初始化。
4. runtime 只消费结构化 observation，确认 guest 网络是否收敛。

libvirt 可以用显式 `cloud_init` 生成 NoCloud seed，也可以使用预配置镜像。Firecracker 和 Docker 实现自己的 guest/network 行为；runtime 不理解 netplan、NoCloud 等 provider 细节。

IPv6 address-family 接口已经预留，但当前文档和验收不把 IPv6 描述为生产可用能力。真实 IPv4 异构通信结果见 [Batch 4 Network Acceptance](verification/2026-07-13-batch4-network-acceptance.md)。

## 结构化 Guest 执行

guest 命令通过结构化请求表达：program、arguments 和明确的 shell mode 分离。runtime 和 provider 不再依赖拼接的 inline shell 字符串作为公共执行合同。

这套合同用于：

- provisioner 执行。
- image entry 执行。
- Docker exec、SSH 和 Firecracker vsock 等不同 transport。
- 验收中的 guest marker、状态检查和通信探测。

结构化执行并不消除 guest 内 shell 的风险；它明确了何时请求直接执行程序、何时显式请求 shell，并避免 provider 各自定义不兼容的命令形态。

## 生命周期与 Reset

典型生命周期是：

```text
validate -> plan -> apply -> observe
                    |       |
                    +-> reset -> observe
                    |
                    +-> destroy
```

- `validate` 检查 HCL schema、引用和图结构。
- `plan` 比较期望拓扑与当前 state，产生确定的资源动作。
- `apply` 按依赖顺序收敛资源，并记录 checkpoint 和 state patch。
- `reset` 从不可变 baseline 替换 guest 的可变状态。
- `destroy` 按逆依赖顺序删除拓扑拥有的资源。

reset 有两种入口：

```bash
# 默认按依赖顺序 reset 整张拓扑的节点。
sysbox -f field.sysbox.hcl reset --auto-approve

# 精确 reset 一个逻辑节点。
sysbox -f field.sysbox.hcl reset \
  --target sysbox_node.web --auto-approve
```

reset 保持以下内容稳定：

- HCL 资源地址和拓扑依赖。
- 声明的 IP、MAC 和 attachment 意图。
- 已 pin 的 artifact baseline identity。
- 非 target 节点的 external identity。

reset 替换以下内容：

- Docker container identity 和可写层。
- Firecracker generation、可写 rootfs、进程和 socket。
- libvirt domain UUID、qcow2 overlay 和 seed。

runtime 负责编排、checkpoint 和最终状态切换；provider 负责 prepare、精确 destroy、apply、observe 和 cleanup。`prevent_destroy` 不阻止 reset，因为逻辑资源仍然存在。

## State、Stored Plan 与恢复

当前 state schema 是 v6。旧 state 在加载时立即报不兼容，不做迁移，也不保留双路径读取。用户必须使用对应旧版本先 destroy，再用当前版本 recreate；无法 destroy 时，需要明确删除旧 state 并自行处理遗留外部资源。

严格升级规则避免新 runtime 在缺少 ownership、artifact 或 guest identity 字段时猜测旧状态含义。typed state 合同见 [Typed State](architecture/typed-state.md)。

stored plan 绑定以下内容：

- HCL revision 和计划动作。
- 创建计划时的 state serial。
- reset 使用的 baseline digest。
- 可验证的 plan fingerprint。

state 已变化或计划 fingerprint 不匹配时，执行会被拒绝。详细合同见 [Stored Plan Contract](architecture/stored-plan-contract.md)。

apply、reset 和 destroy 会记录 operation checkpoint、substep 和 state patch。reset 在破坏旧 guest 前持久化 provider opaque handle，并在 replacement apply、NIC wiring 和 start 后继续 checkpoint。进程失败后，下一次执行可以从已确认的阶段恢复，而不是盲目重做整条路径。

## 所有权与破坏性安全

Sysbox 需要宿主机高权限，因此破坏性操作必须证明目标属于当前拓扑和 generation。关键检查包括：

- Docker 使用持久化的精确 container ownership anchor。
- Firecracker 使用 PID、进程 start time、VM identity 和 socket anchor，并拒绝不完整的旧 anchor。
- libvirt 同时验证 domain UUID、domain XML 中的 overlay 路径，以及 VM 目录内绑定 domain/UUID 的 ownership manifest。
- network cleanup 使用拓扑范围的 netns、bridge、veth 和 TAP identity。
- reset handle 只序列化非 secret 的恢复和所有权数据；secret 在每次执行时重新解析。

本地 state 默认使用文件锁和原子替换。多 Agent 部署推荐 Postgres 的 CAS、advisory lock 和 snapshot；不具备相同保证的 HTTP/S3 backend 会明确暴露安全能力差异。见 [Backend Safety](architecture/backend-safety.md) 和 [Secrets](architecture/secrets.md)。

这些检查降低误删同宿主机资源的风险，但不把 Sysbox 变成针对恶意 root 用户的安全边界。能够修改进程、state 和 artifact 的宿主机管理员仍处于信任边界内。

## CLI 与服务运行模式

Sysbox 的核心 runtime 有两个操作入口：

### 本地 CLI

CLI 直接读取 HCL 和本地或远端 state backend，适合单机实验、开发和验收。所有生命周期操作走同一 runtime。

### API、Agent 与 Web

可选 API 把 Project、Topology、Revision、Plan、Run、Agent、State 和 Event 暴露为服务对象。Agent 在具备实际宿主机能力的位置执行分配的 run；API 保存控制面元数据和事件。Web UI 提供同一 API 上的操作界面。

服务模式不会引入另一套拓扑执行语义。本地 CLI 和 Agent 都通过同一个 run executor、runtime 和 provider 合同执行。部署方式见 [Deployment](deployment.md)，HTTP 对象和 endpoint 见 [API](api.md)。

## 验收证据

Sysbox 的异构能力有两份当前验收记录：

### Batch 4 网络验收

[Batch 4 Network Acceptance](verification/2026-07-13-batch4-network-acceptance.md) 覆盖 Docker、Firecracker 和 libvirt 共用隔离 IPv4 网络，并验证三个节点之间全部六个有向通信路径、重复 plan 和 destroy 后网络残留。

### Batch 5 reset 验收

[Batch 5 Reset Acceptance](verification/2026-07-14-batch5-reset-acceptance.md) 覆盖：

- 3 次连续整拓扑 reset。
- Docker、Firecracker、libvirt 各一次 targeted reset。
- 每次 reset 后的六向通信。
- guest mutation marker 消失。
- external identity 更新。
- IP、MAC 和 artifact digest 保持稳定。
- 非 target 节点 external identity 不变。
- 最终 destroy 后零拓扑归属残留。

这些结果证明的是仓库记录的 Linux/IPv4/provider 组合，不应外推为任意 guest、任意宿主机发行版或任意网络模式的兼容性声明。

## 当前边界

- 网络正式范围是 IPv4-first；IPv6 仍是扩展接口，不是已验收功能。
- 当前节点 provider 是 Docker、Firecracker 和 libvirt。
- 当前真实异构验收使用 Linux guest、amd64 artifact 和特定宿主机工具链。
- Firecracker 和 libvirt 需要 KVM、镜像准备和额外宿主机工具。
- HTTP/S3 state backend 不提供 Postgres 等价的锁、CAS、snapshot 和 delete 保证。
- Sysbox 不提供 Terraform provider/module 生态兼容性。
- Sysbox 不负责实验上层的检测、评分、归因或研究工作流语义。

## 下一步

- 从 [README quick start](../README.md#快速开始) 运行 Docker-only 拓扑。
- 阅读完整的 [heterogeneous-matrix HCL](../examples/heterogeneous-matrix/field.sysbox.hcl)。
- 按 [Deployment](deployment.md) 部署 API、Agent 和 Web。
- 查看 [Documentation Index](README.md) 进入架构合同和验收报告。
