# Sysbox 设计原则

## 声明意图，而不是拼接命令

HCL 描述节点、网络、artifact、依赖关系和生命周期意图。Runtime 根据资源图规划顺序，provider 负责宿主机具体操作。

## 一个模型覆盖异构 substrate

Docker、Firecracker 和 libvirt 共用资源地址、artifact 身份、网络 attachment、plan、state、reset 和 destroy 语义。公共模型不隐藏 provider 差异；provider 专属配置必须放在对应 `provider` block 中。

## 身份必须稳定且可审计

逻辑资源使用 canonical address，例如 `sysbox_node.web`。容器 ID、VM UUID 和 generation ID 是可替换的外部身份。State 明确区分两者，不根据名称猜测所有权。

## 先计划，再变更

所有 mutation 都基于配置、state、schema、driver 和 artifact 指纹。输入变化或 state serial 不匹配时，stored plan 必须拒绝执行。

## 失败是生命周期的一部分

Apply、reset 和 destroy 的关键步骤写入 checkpoint。恢复先观察外部对象，再决定采用、继续或清理；不会把未知状态默认为成功。

## 安全删除优先于方便删除

Provider 只能删除持有完整 ownership 证据的资源。名称相同但 ownership 不匹配的对象必须保留并报错。

## Secret 不进入持久状态

配置只保存 secret reference；解析值仅在执行边界存在，不写入 plan、state、checkpoint、事件或日志。

## 受控范围换取确定性

Sysbox 不是 Terraform 兼容层，也不是通用云编排器。它聚焦单机或受控宿主机上的 Linux 实验拓扑，以有限资源集合换取严格的状态和恢复语义。
