# firewall 用 nsenter 替代 setns+netlink —— 重构设计

日期：2026-09-10
状态：三步已实施（读/写均走可杀的 nsenter 子进程 + `sysbox-netns` helper；能力收窄独立成后续）

## 一、背景与问题

firewall 现在在 agent 主进程里做 netns 操作：

1. `os.Open("/proc/<pid>/ns/net")`（要 `SYS_PTRACE`）
2. `setns(fd, CLONE_NEWNET)`（要 `SYS_ADMIN`）
3. `nftables` 库（`mdlayher/netlink`）写/读（要 `NET_ADMIN`）

由此带来两个结构性问题：

- **读卡死**：`mdlayher/netlink` 的 `Receive()` 无 deadline，读 dump 撞活跃容器的
  netfilter 锁就永久挂。为绕过它，firewall 改成了「只写不读」——apply 写后不读回、
  observe 只看 target 存活。这违背了架构文档「Apply 只有在 readback 成功后才完成」
  的契约，也让 firewall 失去了运行期漂移检测。
- **capability 常驻**：agent 主进程持续持有 `SYS_ADMIN + SYS_PTRACE + NET_ADMIN`，
  任何一个 netlink 读卡死都会拖死整个 agent。

## 二、方案：把 netns 操作隔离到可杀的 nsenter 子进程

```
nsenter --net=/proc/<pid>/ns/net nft -f /tmp/ruleset.nft   # 写
nsenter --net=/proc/<pid>/ns/net nft list ruleset           # 读
```

核心不是「权限变少」，而是**把不可中断的 netlink 操作隔离到子进程**——父进程
`exec.CommandContext` 带 timeout，超时/取消就 `kill` 子进程。卡死不再影响 agent 主进程。

### 关于 capability 收窄（本设计暂不做）

最初设想用 setcap 的 helper 把 firewall 的 capability 从 agent 常驻收窄。这**不可行**：
Docker 的 `cap_add`/`cap_drop` 控制的是容器 **bounding set**（能力硬上限），file capability
和 ambient set 都要与 bounding set 求交集。只要 `SYS_PTRACE` 不在容器 bounding set 里，
同容器内的 helper 即使 `setcap cap_sys_ptrace+ep` 也拿不到它。

要真正收窄，正确模式是：**bounding set 保留该能力，agent 启动时从 effective/permitted 里
drop，helper 用 file cap 取回**——这是独立的 privilege-separation 改造，不在本设计范围。
因此本设计只做「可杀隔离 + 去掉 netlink 库」，agent 仍保留 `SYS_ADMIN + SYS_PTRACE +
NET_ADMIN`（其中 `SYS_ADMIN + NET_ADMIN` 还被 network 驱动的 netns/bridge/veth 需要）。

## 三、实施状态

当前提交已经完成读、写、删三条路径的子进程隔离。

- `ObserveRuleset` 从 `CheckTarget`（存活检查）改回「读 ruleset」：`nsenter --net=... nft list ruleset`，解析输出里的 `comment "sysbox-owner=...;digest=..."` 提取 digest，和 `desired_digest` 比对。
- `FirewallResourceHandler.Read` / `RouterResourceHandler.Read` 恢复「读 digest 判漂移」。
- 解析只作用于目标 table，其他 policy table 不会影响当前资源。

### 写和删

- `compiledRuleset` 渲染为 `nft -f` 脚本，apply 和 delete 都通过 helper 执行。
- 删除只忽略目标不存在；权限、超时、helper 缺失等错误会返回给 runtime。
- NAT 只有在 `Masquerade=true` 时生成 postrouting masquerade 规则。

### helper 和超时

- `/usr/local/bin/sysbox-netns` 是 `nsenter + nft` 的单一入口。
- 每次调用有 15 秒 deadline；取消时终止 helper 进程组，helper 使用 `Pdeathsig` 终止其 nft 子进程。
- 这限制 agent 的等待时间，但无法保证内核处于不可中断睡眠时立即释放内核资源，仍需在目标内核和容器负载下做集成验证。

## 四、影响面

- **HCL / API：零破坏**。`sysbox_firewall`、`provider "docker"`、变量/locals 等全部不变，
  现有拓扑声明无需修改。
- **部署**：agent 镜像加 `util-linux`（nsenter）+ `nftables`（nft）。这是唯一必须动的地方。
- **observe 语义**：从「存活检查」升级为「读 digest 判漂移」。正常收敛路径不变（apply →
  读 → 匹配 → ready），异常场景（规则被篡改/丢失）会被发现为 drift。
- **升级兼容**：旧版本已 apply 的表仍保留 owner/digest marker，新版本按 table 读取 marker；首次升级建议执行一次 apply 验证目标 nft 版本的输出格式。

## 五、性能与稳定性考量

- **进程开销**：每次 firewall 操作一次 `exec` + 输出解析。firewall 是低频操作（apply +
  周期 observe），开销可忽略。周期 observe 的 `nft list` 每次一个短命子进程，比现在
  setns+netlink 的「常驻连接 + 可能卡死」更可预期。
- **超时**：读/写都带 deadline（如 15s），超时 kill 子进程，杜绝永久卡死。
- **并发**：firewall 操作天然低频且按 topology 串行（已有 lockTopology），子进程不引入
  新的并发问题。
- **稳定性验证点**：nft 版本间 `list ruleset` 输出格式是否稳定（comment 字段的位置）。

## 六、验证计划

1. 单测：`nft list ruleset` 文本按目标 table 解析 digest。
2. 单测：`compiledRuleset → nft 脚本`，包含 NAT 开关和规则匹配。
3. 单测：helper 超时/取消、删除错误传播。
4. 可用 `nft` 且具备 `CAP_NET_ADMIN` 时运行 `nft -c -f -` 语法检查。
5. Docker 集成：apply → observe → delete，并验证容器停止后返回 drift。
6. 端到端：用多节点、多 firewall 的自包含拓扑验证，确认 ready 收敛不变。
