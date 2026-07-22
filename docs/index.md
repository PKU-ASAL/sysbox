# Sysbox Documentation

文档按“先完成任务，再理解原理，最后查精确契约”的顺序组织。每类事实只有一个维护位置；其他文档只链接，不复制。

## 第一次使用

1. [Quickstart](quickstart.md)：在 Docker 上完成第一次 validate、plan、apply 和 destroy。
2. [Authoring Topologies](guides/authoring-topologies.md)：把实验需求表达为 image、network、node 和依赖。
3. [Lifecycle And Reset](guides/lifecycle-and-reset.md)：理解 no-op plan、replacement、reset 和安全销毁。

## 构建实验拓扑

- [Heterogeneous Nodes](guides/heterogeneous-nodes.md)：选择 Docker、Firecracker 或 libvirt。
- [Networking And Policy](guides/networking-and-policy.md)：地址、路由、NAT、alias 和 firewall。
- [Troubleshooting](guides/troubleshooting.md)：从 diagnostics、plan、state 和 checkpoint 定位失败。

## 部署控制面

- [Control Plane Deployment](operations/control-plane-deployment.md)：部署 API、Postgres、Agent 和 Web。
- [Agent Operations](operations/agent-operations.md)：Agent 身份、能力、heartbeat、lease 和升级。
- [Artifacts](operations/artifacts.md)：准备、固定和缓存 kernel、rootfs、qcow2 与 OCI image。
- [Upgrades And Recovery](operations/upgrades-and-recovery.md)：备份、升级、恢复和 residue audit。

## 理解系统

- [设计原则](design-principles.zh-CN.md)：为什么采用声明式图、严格身份和显式 provider 边界。
- [Architecture](architecture.md)：组件、数据流、state、stored plan、driver 与 recovery 的规范性说明。
- [项目建议书](project-proposal.zh-CN.md)：面向立项和汇报的建设目标、技术路线、创新点与验收框架。

## 精确参考

- [HCL Reference](reference/hcl.md)
- [CLI Reference](reference/cli.md)
- [API Reference](reference/api.md)

## 参与开发

- [Contributing](development/contributing.md)
- [Testing](development/testing.md)
- [Releasing](development/releasing.md)

根目录 [README](../README.md) 是项目入口，不承担完整手册职责。
