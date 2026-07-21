# Sysbox 设计原则

本文解释设计取舍。系统实际契约见 [Architecture](architecture.md)，字段和命令见 [Reference](index.md#精确参考)。

## 从问题出发

Sysbox 要解决的不是“如何启动一个容器或虚拟机”，而是：如何让一整张异构实验拓扑在多次运行、失败中断和环境漂移后，仍然可解释、可重复、可恢复。

因此，单次创建速度或 provider 数量不是最高优先级。身份、计划、所有权、观察和恢复必须先成立。

## 声明最终意图，而不是保存命令序列

命令序列描述“曾经做过什么”，无法可靠回答外部对象已漂移时下一步应该做什么。Sysbox 保存资源图和 desired state，通过 observation 比较外部现实，再生成行动计划。

代价是资源类型必须有明确 schema，不能接受任意脚本作为核心生命周期语义。脚本仍可作为 provisioner，但不拥有资源身份和删除权。

## 统一公共语义，保留真实差异

Docker、Firecracker 和 libvirt 都有 node、artifact、network attachment 和 reset，但启动、网络初始化、存储和身份机制不同。Sysbox 统一公共结果，不伪装底层机制相同。

因此 provider 专属配置留在 provider block，runtime 通过 capability 请求能力。为了表面统一而把 Docker `ENTRYPOINT`、Firecracker kernel 或 libvirt cloud-init 塞入公共 node 字段，会制造错误抽象。

## 逻辑身份比外部 ID 更重要

实验中的“数据库节点”需要在 reset 前后保持同一逻辑身份，但容器 ID 或 VM UUID 应随 generation 替换。Canonical resource address 表达用户意图，external ID 表达当前实现对象。

二者分离使 targeted reset、state move、drift observation 和审计能够同时成立。

## 未知不是缺失

Provider 暂时不可达、权限不足或 observation 失败，不能推断资源不存在。把 unknown 当作 absent 会导致危险的重复创建和误删除。

因此 observation 使用 `present`、`absent`、`drifted`、`degraded`、`unknown` 等显式状态；unknown 阻止 mutation，要求操作者先恢复可观测性。

## 删除必须比创建更严格

创建失败通常留下可见残留；错误删除可能破坏用户资源。Sysbox 要求 provider 在删除前重新验证 ownership anchor，不以名称相同作为充分证据。

这也是旧 state 被严格拒绝而不是自动猜测迁移的原因：缺失 ownership 和 identity 字段时，保守失败优于便利但危险的清理。

## 失败恢复是正常路径

宿主机操作无法组成数据库事务。Sysbox 通过 durable checkpoint 记录不可逆步骤，并在恢复时先观察外部对象，再采用、继续或清理。

恢复逻辑与正常生命周期同等重要；没有 checkpoint 和幂等恢复的 provider 不能被视为完整实现。

## 安全来自显式边界

Secret reference 与明文、公共 state 与 provider-private state、core policy 与物理设备、API 调度与 Agent 权限都必须分离。边界越模糊，日志泄密、状态耦合和权限扩散越难避免。

## 有限范围是主动选择

Sysbox 不追求任意 Terraform provider、任意云资源或任意 guest OS。它以受控的 Linux 实验环境换取可验证的生命周期语义。扩展范围必须先证明身份、观察、恢复和安全删除契约，而不是只证明“能启动”。
