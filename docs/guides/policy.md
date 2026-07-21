# Policy Guide

Sysbox 的 `sysbox_firewall` 将声明式 IPv4 policy 挂载到 node 或 router。规则使用逻辑 attachment，而不是宿主机设备名。

## Model

- 明确 direction、verdict 和 protocol。
- 使用 source/destination CIDR 与 port range。
- 使用 connection state 表达返回流量。
- NAT 属于 router/network 意图，不应通过 provisioner 修改宿主机 iptables。
- Provider 原子替换 topology-owned nftables table，并在 apply 后 readback。

## Operational Rules

1. 默认拒绝策略必须显式允许管理和返回路径。
2. 不要在 provisioner 中运行 `iptables`、`nft` 或 `nsenter`。
3. Policy attachment 必须引用当前 topology 的 node/router。
4. IPv6 policy 当前会验证失败，不能依赖隐式降级。
5. Destroy 只删除 ownership marker 完整匹配的 table。

真实特权测试覆盖 default deny、端口 allow、established/related 返回流量、router forwarding、NAT、readback 和零残留。运行方式见 [Testing](../development/testing.md)。
