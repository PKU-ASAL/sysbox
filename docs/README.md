# Sysbox Documentation

这里收录 Sysbox 当前的产品文档、架构契约和验收证据。仓库根目录的 [README](../README.md) 是使用入口；需要完整理解资源模型、生命周期和可信性边界时，从 Overview 开始。

## 从这里开始

- [Sysbox README](../README.md)：产品定义、异构 HCL、能力矩阵、快速开始和常用命令。
- [Sysbox Overview](overview.md)：拓扑模型、provider 边界、artifact 身份、网络、结构化执行、reset、state 与恢复。
- [Heterogeneous Matrix Example](../examples/heterogeneous-matrix/README.md)：Docker、Firecracker 和 libvirt 共用 IPv4 网络的真实示例与运行要求。

## 操作 Sysbox

- [Deployment](deployment.md)：Compose profiles、`.env`、API/Agent/Web 部署、state backend 和 artifact 挂载。
- [API](api.md)：Project、Topology、Revision、Plan、Run、Agent、State、Event 和 action API。
- [Firecracker Artifacts](firecracker-artifacts.md)：准备和缓存 Firecracker kernel/rootfs artifact。
- [Releasing Sysbox](releasing.md)：GitHub Actions、GHCR、版本 tag、binary/OCI 发布与失败恢复。

## 架构契约

- [Resource Addresses](architecture/resource-addresses.md)：资源身份、引用和重命名规则。
- [Typed State](architecture/typed-state.md)：结构化 state 和 provider opaque state 边界。
- [Stored Plan Contract](architecture/stored-plan-contract.md)：stored plan、state serial 和执行一致性。
- [Backend Safety](architecture/backend-safety.md)：backend 的锁、CAS、快照和并发安全能力。
- [Secrets](architecture/secrets.md)：secret 解析、持久化和输出边界。
- [Handler and Driver Contracts](architecture/handler-driver-contracts.md)：runtime handler、driver 和 provider 所有权。

## 验收证据

- [Batch 4 Network Acceptance](verification/2026-07-13-batch4-network-acceptance.md)：三 provider 六向 IPv4 通信、网络收敛和 residue audit。
- [Batch 5 Reset Acceptance](verification/2026-07-14-batch5-reset-acceptance.md)：3 次整拓扑 reset、3 次 targeted reset、身份稳定性和零归属残留。

`docs/superpowers/` 保存设计 spec 和实施计划，用于追踪工程决策，不作为当前产品使用文档。历史发布记录位于 `docs/archive/`。
