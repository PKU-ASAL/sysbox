# Sysbox Architecture

本文是 Sysbox 系统边界和持久契约的规范性说明。操作步骤位于 guides/operations，字段和 endpoint 位于 reference。

## System Context

Sysbox 接收 HCL topology intent，解析为 typed resource graph，结合 durable state 与外部 observation 生成 plan，再通过 capability driver 修改宿主机资源。

```text
HCL / variables / secret references
              |
              v
decoder -> typed graph -> planner -> ordered actions
                                      |
                                      v
                           checkpointed executor
                         /        |        |       \
                    Docker  Firecracker  libvirt  network/policy
                                      |
                                      v
                          typed state + events
```

本地 CLI 与 API/Agent 模式共享 decoder、planner、executor、state manager 和 provider。API 只提供产品对象、调度与远程执行桥接，不定义另一套拓扑语义。

## Configuration And Graph

顶层配置由 substrate、variable、locals、module、data、resource 和 output 组成。Resource reference 与显式 `depends_on` 形成有向图；create 按拓扑顺序执行，destroy 按逆序执行。

Resource handler 拥有某一资源类型的 schema、解码、验证、依赖提取、planning、observation、import normalization 和生命周期规则。Graph 和 planner 不通过字符串字段猜测资源行为。

## Canonical Resource Identity

配置展开、graph、plan、state、checkpoint、CLI、API 和日志使用同一 canonical address：

```text
sysbox_node.web
sysbox_node.web[0]
sysbox_node.web["blue"]
module.lab.sysbox_network.dmz
module.segment["red"].sysbox_node.target[1]
```

字符串 key 使用 JSON quoting。Module path 和 instance key 保持结构化，绝不展平为下划线名称。Malformed address 在进入 state/provider 前被拒绝。

Canonical address 是逻辑身份；container ID、Firecracker generation ID、libvirt domain UUID、network namespace 和 nftables table 是 external identity。Reset 可以保留前者并替换后者。

资源地址变化默认产生 delete/create。需要保留关联时必须显式执行 state move：

```bash
sysbox state mv 'sysbox_node.web[0]' 'sysbox_node.web[1]'
```

## Artifact Identity

OCI image、rootfs、qcow2 和 kernel 作为独立资源进入 graph。Artifact 公共身份包含 kind、source、architecture、guest family、可选 size 与不可变 digest。

Planner 将实际 digest 纳入 stored-plan fingerprint。Apply/reset 在 destructive operation 前重新验证 baseline。大 artifact 不进入 Sysbox runtime image；由 topology、cache 或明确挂载提供。

## Handler And Capability Driver Boundary

Handler 定义“资源意味着什么”；driver 定义“如何操作外部系统”。Driver descriptor 只声明实现的 capability，consumer 通过 registry 请求能力。Planning 在 mutation 前验证所需 capability 是否存在。

关键 capability 包括 artifact、managed network、node lifecycle、NIC、node state、guest execution、policy、reset 和 import。Runtime 不导入 Docker、Firecracker、libvirt 或 nftables 的具体实现。

`pkg/substrate` 只承载中立 wire/execution data types，不负责 registry 或 driver selection。

## Node And Provider State

Node state 分为：

- 公共 typed attributes：address、external ID、artifact identity、connection、attachments、observation。
- versioned provider-private envelope：provider 恢复和删除所需的 opaque identity。

只有 `NodeState` capability 可以编码和解码 private envelope。Runtime 不解释 container name、VM socket、overlay path、domain XML 或进程 identity。

## Network Attachments

Attachment 公共身份是 `(owner resource address, logical attachment name)`。Core 保存 network address、IP prefixes、MAC、gateway、aliases 和 latest observation；NIC capability 独占物理实现及 opaque state。

Runtime 不分配或解释 guest `ethN`、veth、TAP、namespace、Docker endpoint 或 libvirt device name。Guest network initialization 在 attachment wiring 后由 provider capability 执行。

## Policy

`sysbox_firewall` 和 router NAT 使用 typed Policy capability。Core 保存 IPv4 policy semantics、逻辑 attachment reference 和 desired digest；provider 在执行时解析实际设备并原子替换 topology-owned nftables table。

Apply 只有在 readback 成功后才完成。Refresh 比较 semantic digest；动态 packet/byte counter 不进入 digest。Destroy 验证完整 owner marker 后删除。Runtime 不执行 `iptables`、`nft` 或 `nsenter`。

Policy 当前仅支持 IPv4。IPv6 input 明确验证失败，不静默忽略。

## Plan Model

Plan 是唯一有序的 action list：

```text
create  read  no-op  replace  delete  unknown
```

`replace` 先删除 prior object，再创建 desired object。只有 handler 实现真实 in-place update 后才可产生 update。`unknown` 不可 apply。`lifecycle.prevent_destroy` 使 delete/replace planning 直接失败，而不是生成部分拓扑。

每个 action 使用 canonical address、明确 reason 和 schema-owned diff。Dependency 变化是否替换由 dependent handler 决定，不自动级联。

## Stored Plan Integrity

Stored plan 绑定：

- exact HCL bytes and evaluated non-secret input digest;
- state lineage and serial;
- resource/schema versions;
- selected driver identities and capability contract;
- artifact digests;
- ordered actions.

Apply 在调用 provider 前逐项比较。任一 mismatch 返回 field-specific stale-plan error，不进行外部 mutation。Secret 明文和 provider-private payload 不进入 plan。

## State Contract

Durable state 保存 canonical address、resource/schema identity、external ID、typed public attributes、dependencies、attachments、observation status 和 UTC timestamps。Driver detail 使用 versioned private envelope。

Observation status 包括：

- `present`：对象存在且与已知状态一致；
- `absent`：对象确定不存在；
- `drifted`：对象存在但偏离声明或 state；
- `degraded`：对象可观察但功能不完整；
- `unknown`：无法可靠观察。

Unknown 阻止 apply，不触发 replacement。旧 state 缺少当前 schema 所需 identity/ownership 时，在任何 mutation 前拒绝；用创建它的旧 binary destroy 后再 recreate。

## Backend Safety And Concurrency

Backend 分别声明 locking、compare-and-swap、snapshot、deletion、lease 和 force-unlock capability。Mutation 默认同时要求 locking 与 CAS。

Local、SQLite 和 Postgres 满足核心 mutation contract；HTTP/S3 兼容 backend 不具备同等级保证。`--allow-unsafe-state` 仅在调用者能够保证单写者时绕过检查。

State manager 在锁内读取 serial，通过 CAS 保存新版本，并在 destructive operation 前创建 snapshot。API run lease 与 state lock 是两层不同保护：前者防止重复调度，后者保护 durable topology state。

## Checkpointed Execution

宿主机 mutation 无法组成单一数据库事务。Executor 在关键步骤前后持久化 operation checkpoint，包括资源 address、action、provider handle、attachment/private state 和已完成 substep。

恢复流程：

1. 验证 topology、plan fingerprint 和 state lineage。
2. 读取 checkpoint，不根据日志文本推断进度。
3. 通过 handler/driver 观察外部对象。
4. 根据观察结果采用已完成对象、继续剩余步骤或清理残留。
5. CAS 保存 state，再完成旧 generation cleanup。

Repeated recovery 必须幂等。Unavailable 或 invalid observation 停止恢复；missing attachment 可被记录为 drift，以受控 replacement 收敛。

## Reset Contract

Reset 从 immutable baseline 替换 mutable guest generation：

1. 解析 target 和依赖影响范围；
2. 校验 baseline digest；
3. checkpoint provider reset handle；
4. 创建 replacement；
5. wiring attachment、guest network 和 route；
6. 启动并 observation；
7. 原子切换 state external identity；
8. 验证 ownership 后清理 prior generation。

声明的 resource address、MAC、IP 和 artifact identity 保持稳定；external ID 必须变化。Targeted reset 不替换不相关节点。

## Ownership And Destruction

每个外部对象持有 topology、resource address、resource type 和 run/generation 等 ownership anchor。Provider 在 destructive operation 前将持久 state 与实际对象标记比对。

名称相同、路径相似或对象位于默认目录都不足以证明 ownership。证据不完整时保留对象并报错。Destroy 的完成条件包括 state 收敛和 topology-owned residue audit。

## Secret Boundary

`env("NAME")` 产生 `secret://env/NAME` reference，不在配置求值时读取明文。Execution-scoped resolver 在 provider operation 前解析；缺失值使执行失败。非敏感可选路径使用 `env_optional()`。

Plan、state、checkpoint、API payload、event 和日志只能保存 reference 或 redacted placeholder。Node environment、connection credential、provisioner command、authorized key 和 provider config 都经过相同 resolver 边界。

## Control Plane And Agents

API 持久化 Project、Workspace、Topology、Revision、Plan、Run、Agent、Event、Artifact 和 projection。Run 根据 capability 调度给 Agent；Agent 通过 signed protocol 领取 command、claim/renew lease、执行共享 runtime 并回传 event/projection。

API container 不应持有 Docker socket、KVM 或 libvirt 权限。Host Agent 持有最小必要 capability。Topology state/checkpoint 默认位于执行 Agent，除非配置共享安全 backend。

## Architectural Invariants

- 未通过 plan fingerprint 和 backend safety 检查前，不调用 provider mutation。
- Unknown observation 不等于 absent。
- Secret plaintext 不进入 durable data。
- Runtime 不解释 provider-private state。
- 删除前重新验证 ownership。
- Reset 在 replacement 可观察且 state 保存成功后才清理 prior generation。
- CLI 与 API/Agent 使用同一执行语义。
