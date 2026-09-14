# Sysbox 生命周期执行闭包与异构能力设计

## 目标

让每个 run 的配置、执行位置、state、checkpoint 和宿主机资源形成可验证的闭包，避免共享可变 workspace、动态迁移 Agent 和不完整 state 导致错误执行或误删；同时把异构 provider 的 namespace、网络初始化和真实观察约束前移到 plan/preflight。

## 分阶段范围

### 第一阶段：执行闭包

每个 apply/repair/reset/destroy run 必须保存不可变 revision 引用和执行快照目录。API 在创建 run 时 materialize `runs/<topology>/revisions/<revision>/`，Agent 通过 run 的快照路径读取 HCL 与模块文件。现有 topology workspace 继续作为发布和兼容读取入口，但执行路径不再依赖它。快照按 revision digest 去重，写入采用临时目录加 rename。

### 第二阶段：placement

新增 topology placement 持久化记录，包含 AgentID、协议版本、绑定时间和状态。首次创建资源的 run 确认 placement；同一 topology 后续生命周期操作只能使用该 Agent，显式指定其他 Agent 时返回冲突。尚无 placement 的纯 validate/plan 不创建绑定。placement 缺失或 Agent offline 时 fail closed；迁移不在本阶段隐式实现。

### 第三阶段：ownership 与清理

GC、workspace delete、destroy 和 recovery 使用统一 ownership 判定。state 不存在、损坏、backend 不可达或 topology 清单不完整时，GC 和普通 delete 均拒绝执行并保留材料。强制删除仍需显式 `force`，并记录原因；GC 不将读取错误当作空集合。

### 第四阶段：异构 capability 与连接

扩展 capability 描述 NIC cold/hot plug、guest network initialization 粒度、可访问 namespace 集合和真实 observation 能力。Firecracker 在 plan 阶段拒绝跨 namespace 的多网络节点，或把所有 TAP 收敛到单一可访问 namespace；每个 NIC 都必须有 guest IP 初始化记录。引入统一 ConnectionContext，包含 namespace、host/port、SSH credentials、known_hosts 与 host-key policy，Exec/background/copy/console 全部从它派生。Provider attachment Observe 必须查询真实接口/domain，查询失败返回 Unknown。

## 数据流与兼容性

API 创建 run 时校验 revision、placement 和 state safety，持久化 run 后派发只含非敏感输入的命令。Agent claim 后取得 transient inputs、snapshot 路径和 placement 校验结果，执行器只从 snapshot 加载 workspace。state 仍由 backend locking/CAS 保护；snapshot 与 state serial 一起写入 checkpoint。旧 run 没有 snapshot 时只允许显式 repair/recovery 迁移，不静默回退到共享 workspace。

## 错误处理

- snapshot materialize 失败：run failed，workspace 和旧 snapshot 保留。
- placement 冲突或不可用：run conflict，禁止 fallback 到其他 Agent。
- state 读取错误：delete/GC 返回错误，不删除任何 state/workspace。
- provider 无法确认真实状态：返回 Unknown，由 recovery policy 决定，不报告 Present。
- 不满足异构 capability：plan/preflight 失败，禁止进入 apply。

## 测试验收

1. 两个不同 revision 并发 apply 时，每个 run 读取自己的 snapshot，互不覆盖。
2. topology 首次绑定 host-b 后，未指定 Agent 的后续 run 仍发往 host-b；host-a 请求返回冲突。
3. 损坏或缺失 state 时，普通 delete 和 GC 均保留文件并返回错误；完整清单下仍能清理真正孤儿。
4. Firecracker 两张 NIC 都生成 guest 初始化记录；跨 namespace 组合在 plan 阶段被拒绝或按单 namespace 方案执行。
5. libvirt 的 exec/background/copy/console 使用同一 namespace 和 host-key policy；外部移除 domain NIC 后 refresh 返回 drift/unknown。
6. 保持现有 `go test ./...`、`go vet ./...` 和相关 `go test -race` 门禁通过；具备宿主机能力时再运行 heterogeneous matrix/reset。

## 非目标

本设计不实现跨 Agent overlay network、自动 topology migration、Windows WinRM、Firecracker jailer 或 IPv6 policy；这些仍需独立设计。Windows 属于正式目标范围，但 guest execution、网络初始化等能力必须由 provider capability 明确声明后才可使用。
