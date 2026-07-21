# Sysbox Documentation

Sysbox 是面向 Linux 实验环境的声明式拓扑控制面。它使用同一套 HCL、计划、状态和生命周期模型管理 Docker、Firecracker、libvirt 与 Linux 网络资源。

## Start Here

- [Quickstart](quickstart.md)：安装 CLI，运行第一个 Docker 拓扑。
- [设计原则](design-principles.zh-CN.md)：理解 Sysbox 为什么这样设计以及明确不做什么。
- [Architecture](architecture.md)：资源图、runtime、provider、state、reset 和恢复模型。

## Guides

- [Policy](guides/policy.md)：网络策略、流量方向、NAT 和安全边界。
- [Agent Management](guides/agent-management.md)：注册、运行和维护 Agent。
- [Investigation](guides/investigation.md)：使用 plan、state、run、event 和 checkpoint 定位问题。

## Operations

- [Deployment](operations/deployment.md)：部署 API、Agent、Web，以及准备 VM artifact。
- [Maintenance](operations/maintenance.md)：升级、备份、发布与故障维护。

## Reference

- [Configuration](reference/configuration.md)：HCL、substrate、resource 和 provider 配置。
- [API](reference/api.md)：HTTP API 对象与 endpoint。
- [CLI](reference/cli.md)：命令、全局参数和退出行为。

## Development

- [Development](development/development.md)：本地开发、代码边界和贡献流程。
- [Testing](development/testing.md)：单元、集成、特权与异构验收。

仓库根目录的 [README](../README.md) 只保留项目介绍和最短入口；本目录是正式文档的唯一导航。
