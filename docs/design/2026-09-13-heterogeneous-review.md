# 异构节点支持专项审查

审查日期：2026-09-13。基于 commit `e359226`。范围为 Docker、Firecracker、libvirt 的节点生命周期、网络、guest 操作、观察与 reset。以下发现待修复；本轮没有修改实现代码。

前一轮控制面与 GC 问题见 [项目审查记录](2026-09-13-project-review.md)。

## 支持范围

| 能力 | Docker | Firecracker | libvirt |
|---|---|---|---|
| 运行形态 | Linux 容器 | Linux KVM microVM | QEMU/KVM VM |
| 镜像 | OCI | ext4 rootfs + kernel | qcow2 |
| 网络 | Docker endpoint 或 veth | TAP，启动前接入 | domain NIC，启动前接入 |
| Guest exec / 文件写入 | Docker API | vsock RPC，或 SSH | SSH |
| Console 实现 | Docker attach/exec | vsock 或 SSH | SSH；不是直接读取 serial console |
| Reset | 从固定镜像重建容器 | 新 VM generation / rootfs | 新 domain UUID / overlay |
| Pause / Resume | Docker API | Firecracker 进程 SIGSTOP/SIGCONT | virsh suspend/resume |
| Host port exposure | 支持，要求 managed NAT network | 未支持；direct exposure | 未支持；direct exposure |

共同模型已有 image、logical attachment、IP/MAC、route、state、checkpoint 与 reset。但接口存在不代表各 provider 的实现和验证深度完全一致。

Windows 10 目前仅为 libvirt 实验性的 domain-start lifecycle：要求 preconfigured 网络初始化，文档明确不保证 guest readiness、WinRM、Windows 网络配置或 provisioner。当前 libvirt XML 固定 x86_64 和 qemu-system-x86_64；不能把 CLI 的 arm64 分发等同于 libvirt ARM64 VM 支持。Docker 镜像的 architecture 被按声明写入 identity，当前 ResolveImage 没有核验 inspect 的实际架构，也没有按声明选择 pull platform。

通用 vcpus/memory 在 Docker 与 Firecracker 生效；libvirt CreateNode / reset 使用 provider 块内的 vcpus/memory，未读取 NodeSpec 的对应通用字段。编写混合拓扑时不能假设只改 substrate 就能保持资源配额语义。

## HET-01 / P1：Firecracker 第二张及后续网卡没有自动配置 IP

- 位置：`pkg/provider/firecracker/nic.go:160`。
- `upsertCmdlineArg` 已支持多个 ip=，guest init 也会遍历它们，但实际 Attach 调用仍以 `nicIdx == 0` 为条件注入 IP。
- 触发：声明多个 link，且依靠 Sysbox 为 guest 初始化网络。
- 影响：第二张及后续网卡没有对应 IP 配置，相关通信和路由失败。即使所有 TAP 位于同一 namespace，仍有此问题。
- 验证：通过临时 overlay 测试调用真实 Attach，以无副作用的假 ip 命令隔离宿主机操作。生成配置包含两个 network-interface，却只有 eth0 的 ip=。
- 修复方向：为每张网卡生成 IP 初始化信息，并通过 Attach 路径测试；只测字符串 helper 无法覆盖这个错误。

## HET-02 / P1：Firecracker 无法访问分属不同隔离网络的 TAP

- 位置：`pkg/provider/firecracker/nic.go:129`、`pkg/provider/firecracker/node.go:286`。
- Attach 把每张 TAP 放进目标 network 的 netns；HandleState / vmProcess 只记录第一个 netns，StartNode 只在该 namespace 内启动 VMM。
- 触发：同一个 Firecracker 节点连接两个独立的隔离 network。
- 影响：VMM 无法打开位于其他 namespace 的 TAP，不能完成这类多网段节点的启动。补齐 HET-01 的 IP 参数不足以修复此问题。
- 验证：模拟两次 Attach 确认目标 netns 不同，但 VM 仍固定在第一个 netns；启动失败结论来自 namespace 与 StartNode 调用链分析，未启动真实 Firecracker。
- 修复方向：为所有 NIC 提供 VMM 可访问的统一 namespace/bridge 接入方案；实现前应在 plan 阶段拒绝该组合。

## HET-03 / P1：libvirt 忽略显式 SSH 主机身份验证配置

- 位置：`pkg/provider/libvirt/exec.go:98`、`pkg/transport/ssh.go:33`。
- Connection 处理 host/user/password/key，却忽略 KnownHosts 与 InsecureSkipHostKeyCheck；选用的两个 SSH constructor 均设 insecureHost=true。
- 触发：为 libvirt 节点设置 known_hosts，并要求严格验证主机身份。
- 影响：仍使用 StrictHostKeyChecking=no 和 UserKnownHostsFile=/dev/null，与 HCL 文档的默认验证契约相反。
- 验证：通过 Connection 传入显式信任文件和 false 的 insecure 标志，返回连接仍为 insecure=true、knownHosts 为空。
- 修复方向：在保留 netns 的同时传递信任配置；让 provider 的 Connection 路径接受契约测试。

## HET-04 / P1：SSH console 和后台执行丢失 network namespace

- 位置：`pkg/transport/ssh.go:87`、`pkg/transport/ssh.go:128`、`pkg/transport/console.go:68`。
- 普通 Exec 经 command/namespacedCommand 进入 guest 所在 netns；ExecBackground 直接启动 ssh，OpenConsole 则只传 sshArgs 给直接启动 ssh 的 console helper。
- 触发：libvirt guest 仅能从隔离 network namespace 访问，调用 console 或 background provisioner。
- 影响：普通 exec 可用，但 console / background 从宿主机 root namespace 连接，导致失败；如果 root namespace 存在相同地址，也可能连接错误 guest。
- 验证：静态核对 Connection → OpenConsole / ExecBackground → exec.CommandContext 的完整调用链。未连接真实 SSH 服务。
- 修复方向：所有 SSH 操作共用携带 namespace、认证及信任配置的命令创建路径。

## HET-05 / P2：libvirt 网卡观察没有读取真实 domain / NIC 状态

- 位置：`pkg/provider/libvirt/network.go:38`。
- Observe 只比较 persisted HandleState.Bridges 与 persisted attachmentState 的 bridge/MAC，没有查询 domain XML、实际接口或桥接关系。
- 触发：VM 仍运行、UUID 不变，但有人在 Sysbox 外移除网卡或更改其桥接关系。
- 影响：refresh 仍可能把 attachment 判断为 present，不能发现并修复该 drift。ObserveNode 只核对 domain 状态和 UUID，没有补上此检查。
- 验证：用不存在的 domain/bridge 构造匹配的两份 state，Observe 仍返回成功。
- 修复方向：读取真实 domain/interface 状态，区分 missing、drift 和查询失败的 unknown。

## 现有测试能证明什么

- heterogeneous-matrix 示例是三个节点各接同一个隔离网络，不能覆盖 Firecracker 跨两个 netns 的场景。
- matrix/reset 验收脚本覆盖六向 IPv4 通信、重复 plan、三次完整 reset、定向 reset 与清理断言；libvirt guest 操作直接调用 ip netns exec ssh，不能验证 Sysbox 的 console / background 路径。
- Firecracker 多 ip= 单元测试只覆盖 helper，未覆盖 Attach 是否对每张网卡调用该 helper。
- 部分 libvirt console / guest operation 测试检查的是接口存在，而非真实连接行为。

## 本轮验证

- provider、transport、runtime、substrate、driver 单元测试通过；cmd/sysbox-init 当前无测试文件。
- 临时 overlay 复现通过，确认 HET-01、HET-02 的配置问题、HET-03 与 HET-05。所有宿主机网络命令均替换为假命令，未操作真实 VM 或网络。
- 未执行真实 heterogeneous-matrix / heterogeneous-reset 验收；不据此宣称真实环境通过。
- make docs-test 失败：expectedDocs 与目录树不一致。HEAD 已存在的 docs/design 文档和 docs/reference/portability.md 不在脚本清单，说明本次新增记录前已有该问题；本轮新增两份记录也需纳入后续文档清单维护。
