# firewall observe 卡死 —— nftables 只写不读

日期：2026-09-07
状态：已被 2026-09-10 nsenter firewall 设计取代

本文记录的是 2026-09-07 为规避 nftables dump 卡死而采用的临时 write-only 方案。
当前实现已将读、写、删统一移到带超时的 `sysbox-netns` 子进程，并恢复按目标 table
读取 digest 的 drift 检测。实现和验证要求以
`docs/design/2026-09-10-nsenter-firewall-design.md` 为准。

## 一、背景

cyberfield L3 十五节点联调时，firewall 的 apply 成功（写 nftables 33 步 0 失败、分段
生效），但 observe（读回状态）阶段卡死：`Applied` 永久卡在 `Unknown`，agent 日志停在
某时刻后 8 小时无活动。

## 二、根因

卡点在 `pkg/provider/network/owned_policy.go` 的 `observeOwnedRulesetConn`：

```
conn.ListTablesOfFamily(...) → conn.ListChains() → conn.GetRules(...)
    → receiveAckAware → nlconn.Receive()   // 无 deadline，永久阻塞
```

两层原因叠加：

1. **读是 netlink dump**：observe 读回 ruleset 走 `NFT_MSG_GETTABLE`/`GETCHAIN`/
   `GETRULE` 的 dump（内核遍历 + 回多条消息最后 `NLMSG_DONE`）。数据库类镜像
   （mysql/mongo/postgres/jenkins/grafana）起来后高连接活跃，netfilter/conntrack
   持续更新，dump 的遍历和包处理抢同一批内核锁，锁序不对就永远等不到 `NLMSG_DONE`。
2. **nftables 底层无超时**：`google/nftables` 底层是 `mdlayher/netlink`，`Receive()`
   没有 deadline（`SetReadDeadline` 存在但没人设）。而 sysbox 里另一条 netlink 路径
   （router/bridge 用的 `vishvananda/netlink`）默认 `SocketTimeoutTv = 60s`，所以只有
   nftables 会「永久」卡死，别的 netlink 读最多慢 60 秒。

这解释了三个表面矛盾：apply 成功（写是 ack 路径 + 容器刚起、进程不活跃）、observe 卡
（周期读 + 容器全速跑）、root 容器正常 / 非 root 容器卡（真正分水岭是进程活跃度，不是
`Config.User`）。

内核侧有同类已知 bug（nftables dump 死锁）：syzbot 报告的
`nf_tables_dumpreset_obj` 死锁、`nf_tables: GC transaction race with netns dismantle`。

## 三、方案：nftables 只写不读

死锁根源是「读」不是「写」。apply 的写（单条命令 + ack）是安全的。所以把跨 netns 的
dump 读从所有路径上拿掉，nftables 只写不读：

- **observe（firewall/router 的 Read）**：不再读 ruleset，改成检查 target 是否存活
  （docker `ContainerInspect` + `State.Running`，network 检查 netns 是否存在）。存活 →
  Healthy（信任 apply 写的规则还在），不存活 → Drifted。
- **apply**：写后不再读回验证；`applyCompiledRuleset` 改成「总是先 `DelTable`（忽略
  结果）再 `AddTable` 重建」，幂等。
- **delete**：直接 `DelTable`，忽略结果（not-found 是常态，残留由下次 apply 自愈）。
- **checkpoint recover**：不再读 digest 判断「adopt vs 重新 apply」，改成总是重新
  apply（幂等）。

`Policy` 接口的 `ObserveRuleset` 删除，换成 `CheckTarget`；`RulesetObservation.Inventory`
（读回来才有的清单）连带删除。

## 四、改动清单

- `pkg/driver/policy.go`：`Policy` 接口删 `ObserveRuleset`、加 `CheckTarget`；删
  `RulesetObservation.Inventory` 与 `OwnedObject`。
- `pkg/provider/docker/policy.go`：`CheckTarget` 实现（ContainerInspect + Running）。
- `pkg/provider/network/owned_policy.go`：`CheckTarget`（netns 存在）；`ApplyRuleset`/
  `ApplyRulesetInNetNSFD` 去读、总是重建；`DeleteRuleset`/`DeleteRulesetInNetNSFD`
  去读；删 `observeOwnedRuleset*`/`ownedRulesetExists`/`ObserveRulesetInNetNSFD`。
- `pkg/runtime/firewall.go`：Read 改 `CheckTarget`；RecoverCheckpointResource 总是
  apply。
- `pkg/runtime/router.go`：Read 改 `CheckTarget`。
- `pkg/runtime/checkpoint_hooks.go`：`recoverCheckpointPolicy` 总是 apply。

## 五、trade-off

失去「运行期漂移检测」：若容器内规则被有特权的进程篡改，sysbox 发现不了。在 CTF 场景
下这是可接受的——容器内题面服务是非 root 进程，没有 `CAP_NET_ADMIN`，改不了 nftables；
且 `attach_to` 的容器存活是「规则还在」的必要条件，用存活检查兜底足够。

## 六、内核态操作风险表（触类旁通）

sysbox 里「可能撞内核锁」的操作几乎全是「netlink 的 dump 读」。区分三个维度：

| 维度 | 高风险 | 低风险 |
|---|---|---|
| 卡死 vs 失败 | 无超时的阻塞读（nftables）→ 卡死 | setns/权限 → 原子失败 |
| 容器 netns vs 宿主 netns | 进**活跃容器** netns 读 → 撞 netfilter 锁 | 宿主 netns（bridge/tap）→ 活跃度低 |
| 周期 vs 一次性 | observe 周期读 → 放大撞锁概率 | apply 一次性读 → 概率低 |

具体清单：

| 位置 | 库 | 超时 | 结论 |
|---|---|---|---|
| firewall observe/delete 读 | nftables/mdlayher | **无** | ❌ 已死锁，改为只写不读 |
| firewall apply 读验证 | nftables/mdlayher | 无 | ❌ 同源，已去掉 |
| router 地址读取 `netnsDeviceForIP`、policy 接口列表 | vishvananda | 60s | ⚠️ 有兜底，但进容器 netns 读，apply 一次性，未爆 |
| bridge/tap/veth/nic 的 netlink | vishvananda | 60s | ✅ 宿主 netns，低风险 |
| setns / `/proc/<pid>/ns/net` open | — | 原子 | ✅ 失败即报错，不卡 |
| docker exec 流式读、vsock | HTTP/socket | ctx/timeout | ✅ 非内核态，可中断 |

**约束（加 provider 的护栏）**：任何「进容器 netns 做 netlink dump 读」的操作，要么
只写不读，要么必须带超时；不得引入无 deadline 的 netlink 读。
