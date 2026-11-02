# Sysbox 架构

Sysbox 用 HCL 管理实验环境中的镜像、节点、网络和策略。核心是 **Provider SPI + 资源差分引擎**：观察资源、计算变更、执行并验证结果。安装步骤见 [README](../README.md)，字段和协议见 [接口文档](interfaces.md)。

## 定位与边界

- 一个 topology 由一台执行宿主负责，同一 topology 同时最多执行一个变更。
- 支持 Docker、Firecracker、libvirt；Linux 与 Windows 的能力不同，以 provider 契约为准。
- CLI 和远程消费者使用同一服务入口，没有本地 Apply/Reset/Destroy fallback。
- 业务流程、比赛轮次、审批和计分留在上层系统。当前不提供跨宿主 topology 编排或通用 workflow。
- 升级采用停旧服务、确认并清理旧资源、归档旧数据、部署新版本；不在线接管旧格式运行环境。

## 执行链与模块

```text
CLI / 消费者 → API → 普通用户 Agent → 宿主服务 → runtime → Provider SPI
                                                    └→ guest helper
```

API 接受请求、选择宿主、保存操作状态并验证结果。Agent 只负责通信与转发；宿主执行器读取原始请求和资源记录，校验授权后执行。命令投递成功不等于资源操作成功。

| 模块 | 职责 |
|---|---|
| `config` / `graph` / `model` | HCL、依赖图、计划和观察类型 |
| `runtime` | 资源规划、动作执行、结果与状态提交 |
| `driver` / `provider` / `substrate` | 能力注册、平台实现、资源身份及私有状态 codec |
| `hostexec` | 宿主执行环境、Run 执行与恢复 |
| `hostsupervisor` | 授权检查、受控任务、宿主观察及签名回执 |
| `agentexec` | API 与宿主之间的命令、观察、续期和回执转发 |
| `persistence` / `controlstore` | 生命周期与控制面存储、事务、CAS、锁及维护屏障 |
| `workspace` | 不可变 revision、物化、placement 和引用保护 |

资源核心入口是 `runtime.PrepareOperation`，随后调用 `Plan(ctx)` 和 `Execute(ctx, plan)`。Provider registry 显式传入；API 协议不进入资源规划算法。

## 规划与执行

1. 固定 revision、请求摘要、topology 身份和操作范围。
2. 读取 canonical state 与未完成动作。宿主在 Apply/repair 规划时观察选定范围中原定 no-op 的资源。
3. 确认缺失或漂移的资源进入替换计划；未知观察或身份冲突阻止执行。观察不直接覆盖持久化资源身份。
4. 检查依赖和 `prevent_destroy`。执行前再次核对状态版本、归属与实际对象。
5. 每个动作先保存意图，再调用 provider，最后原子提交动作结果与资源状态。
6. 中断后核对原动作和实际效果；不盲目重放未知创建、脚本或删除。

**远程 Plan 是 revision 与 state 的预览，不执行实时 provider refresh，也不是可保存的执行批准令牌。** Apply 的宿主计划可能发现额外漂移；CLI 确认的是操作、revision 和 target。

当前替换采用先删后建。若计划要求替换父资源，却保留仍依赖它的子资源，规划阶段明确拒绝，不自动扩大删除范围。自动连带替换和先建后删不在当前支持范围。

Reset 有独立的代际替换步骤，保留声明的 IP/MAC 等逻辑身份；它复用同一授权、journal、SPI 和状态提交机制，不是第二套执行引擎。

## 权限与信任边界

| 主体 | 权限与限制 |
|---|---|
| API | 持有 issuer 私钥、控制面存储和用户认证；核验宿主签名 |
| Agent | 普通用户、无 capability、NoNewPrivileges；无 provider socket、数据库凭据或签名私钥 |
| 宿主服务 | root、受限 capability 上限；持有 host 私钥、canonical repository 和 provider 权限 |
| Linux guest task | 默认 UID/GID 65534，受 cgroup、期限和签名授权限制 |
| Windows guest task | 非管理员账户，Job Object 管理进程树，私有 ACL 保护 helper 与 journal |
| Firecracker VMM | 配套 jailer 降权，独立 namespace、chroot、seccomp 和 cgroup |

宿主启动前验证 root 所有的配置、身份、密钥和工具路径，使用固定环境。systemd 模板见 [host](../deploy/systemd/sysbox-host.service) 和 [Agent](../deploy/systemd/sysbox-agent.service)。

API token 用于管理入口；Agent 仅持本身份通信凭据。grant 绑定 session、boot、资源 UID/generation、epoch、操作摘要和期限。撤销与停止确认分开：授权过期、断线或请求取消不能自动成为停止证明。

Console 复用普通用户受控任务。检查、foreground exec、raw file 操作不一律具有持久后台任务的恢复保证，具体边界见[宿主与 guest 协议](interfaces.md#host-protocol)。

### 网络入口与服务重启

`sysbox-netns` 的 root 专用入口在执行宿主 PID 1 的 mount/net namespace 中管理命名 netns。它不带 setuid，不接受任意宿主路径；删除核对 namespace 类型、device/inode，provider 仍负责资源归属。

创建和删除由固定宿主锁串行化；实际子进程继承锁，父进程退出时终止子进程链。服务重启后重新打开原入口，不能创建同名对象冒充恢复。容器内 PID 1 不自动代表外部宿主。

### Firecracker 与 libvirt

Firecracker 要求配套的 Firecracker/jailer 1.15.1、KVM、cgroup v2 和具备 `cgroup.kill` 的内核。联网 VMM 必须使用 named netns，不支持共享宿主 Docker bridge 的组合。

宿主 systemd 单元采用系统服务默认 root 身份，避免已复现的 systemd 255 配置组合丢失实际 CAP_SETUID；NoNewPrivileges 和其他防护仍开启。jailer 最终必须以非 root UID 运行 VMM。

libvirt 磁盘放在独立 provider 目录，允许 QEMU 遍历；宿主密钥和 journal 目录保持私有。Reset 显式设置新目录权限，不依赖服务 umask。

## 状态、授权与恢复

canonical state 确定资源归属，journal 记录在途效果，两者不能互相替代。执行锁、CAS 和操作身份共同防止重复与越权；只凭对象名称或 PID 不能认领、删除资源。

未知结果保留冻结与证据，通过已有 observe、recover、continuation 或 fencing 接口处置。元数据快照不是 VM 磁盘快照；元数据删除也不是资源销毁。详细规则见[恢复契约](interfaces.md#recovery-contract)。

文件、SQLite 和 PostgreSQL 使用相同业务转换。SQLite 单库串行写入；共享 PostgreSQL 支持多 API。存储格式严格校验，不自动读取或迁移旧 lifecycle 数据。

同一物理 SQL 数据库复用 lifecycle 连接池，各 topology 使用独立绑定视图；整体关闭后释放池。不能把 topology URL 参数当作新数据库反复建池。

授权仍使用一份全局 ledger。正常检查、续期和派生签发已减少重复提交，但全局写竞争、历史停止记录增长仍需容量验证。**当前没有按宿主/topology 分片，也没有新增租约体系；是否需要进一步改造由性能证据决定。**

## 发布与验收

CLI、API、宿主和 guest helper 必须来自匹配制品。发布包携带版本、commit、哈希与 metadata；部署不隐式重建源码。验收使用正式 systemd 单元和独立实验环境。

| 已有证据 | 范围限制 |
|---|---|
| Docker、Firecracker API 部署/Reset/回收及 netns 重启保留 | 不等于全部 provider 故障组合通过 |
| Docker 外部删除后相同配置 Apply 补齐 | 不覆盖所有依赖替换场景 |
| Windows Server 2025 原生任务、QGA、guest 重启恢复 | 不代表 Windows 10 或最新制品矩阵全部通过 |
| 干净双虚拟机安装、普通用户权限检查 | 候选制品与后续修复版本须分别核对 |
| 连接池修复后，保留历史的 4 并发、40 次循环通过；120 topology 池回归通过 | 短期增长回归，不是多日稳定性证明 |

仍待完成：修复后的统一发布制品、完整异构与 Windows 矩阵、持续负载与历史规模测试、典型故障恢复矩阵，以及 CyberField 消费者联调。通过之前不宣称全部发布门槛完成。

测试入口见 [Makefile](../Makefile)、[发布脚本](../scripts/release) 和 [验收脚本](../scripts/acceptance)。失败时保留现场；skip、静态检查、同源编译不能替代真实制品验收。

## 历史资料

历史设计与阶段记录以 Git 历史为准。早期文档另存于[归档](archive/2026-10-27-documentation.tar.gz)及[清单](archive/2026-10-27-documentation.json)。历史完成声明不自动适用于当前版本，只维护 README、本文和接口文档三份入口。
