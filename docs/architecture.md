# Architecture

## System Shape

Sysbox 将 HCL 解析为带类型的资源图。引用和 `depends_on` 决定拓扑顺序；runtime 调用 capability driver 创建外部对象，将公共状态与 provider-private envelope 写入 state，并在失败时通过 checkpoint 恢复。

```text
HCL -> decoder/schema -> resource graph -> planner -> executor
                                               |-> Docker
                                               |-> Firecracker
                                               |-> libvirt
                                               `-> Linux network/policy
```

CLI 与 API/Agent 模式共享同一个 planner、executor、state 和 provider 实现。API 不定义第二套拓扑语义。

## Resource Identity

Canonical address 在配置、graph、plan、state、checkpoint、CLI、API 和日志中保持一致：

```text
sysbox_node.web
sysbox_node.web[0]
module.lab.sysbox_node.target["blue"]
```

资源重命名默认是 destroy/create。需要保留外部对象时，应显式使用 state move，而不是依赖名称猜测。

## Handler And Driver Boundary

Resource handler 拥有 schema、解码、验证、planning、observation 和 import normalization。Capability driver 拥有宿主机操作。Handler 不导入具体 provider，runtime 只请求所需能力。

Node state 由公共 typed attributes 和 provider opaque state 组成。只有 `NodeState` capability 可以编码或解码 opaque state。Network attachment 的公共身份是“owner resource address + logical attachment name”；物理 veth、tap、Docker endpoint 和 libvirt device name 由 provider 管理。

## Artifacts

`sysbox_image` 和 `sysbox_kernel` 是独立资源。OCI image、rootfs、qcow2 和 kernel 都有明确的 kind、architecture、guest family、source 与 digest。节点引用 artifact 资源，而不在 node block 中隐式下载可变输入。

## Networking And Policy

`sysbox_network` 声明 CIDR 和 NAT 意图。Node/router attachment 声明逻辑接口、地址、MAC、gateway 和 alias。Linux network provider 将意图映射为 bridge、namespace、veth 或 TAP。

Policy 当前为 IPv4。Core 持有逻辑规则和 desired digest，provider 解析实际设备并原子应用 topology-owned nftables table。规则 readback 不包含动态 counter 值，避免流量造成假 drift。

## Planning And Stored Plans

Plan 记录配置、变量、state serial、schema、driver 和 artifact 指纹。执行 stored plan 前重新验证全部指纹；任一输入变化都会拒绝 mutation。Plan action 使用 canonical address，不保存 secret 明文或 provider-private state。

## State And Concurrency

State backend 显式声明 locking、CAS、snapshot、lease 和 force-unlock 能力。Mutation 默认要求 locking 与 CAS。Local、SQLite 和 Postgres 满足该约束；不具备相同能力的 backend 只能通过显式 unsafe override 使用。

State schema 是严格契约。无法安全推断 ownership 或 identity 的旧 schema 会被拒绝，不做静默迁移。

## Reset And Recovery

Reset 从不可变 baseline 创建新 generation，重新连接网络并观察健康状态，最后才切换 state。旧 generation 的删除依赖持久化 ownership anchor。Checkpoint 覆盖破坏前、replacement 创建、NIC wiring、启动、观察和清理阶段，使中断后可以幂等恢复。

## Secrets

Secret reference 可以来自环境、文件或配置的 secret source。解析只发生在执行边界；日志、diagnostic、plan、state、checkpoint 和 API response 必须使用 reference 或 redacted value。

## Control Plane

API 持久化 Project、Workspace、Topology、Revision、Plan、Run、Agent 和 Event 等产品对象。Agent 在目标宿主机领取命令并执行实际 provider 操作。Topology durable state 默认保留在 Agent 宿主机，除非配置共享 backend。

相关操作见 [Deployment](operations/deployment.md)、[Agent Management](guides/agent-management.md) 和 [Investigation](guides/investigation.md)。
