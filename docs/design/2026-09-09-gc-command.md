# sysbox gc 子命令 —— 孤儿资源回收

日期：2026-09-09
状态：历史设计，已被观察驱动重构取代（仅保留安全预览）

## 一、背景

SysField 等编排方在 rollout 被手动 Ctrl+C / SIGKILL 杀掉时，来不及调 sysbox 的
`destroy`，会留下三类孤儿：docker 容器、docker 桥接网络、libvirt VM。现有 `destroy`
只按单个 topology 的 state 清理，扫不到「state 之外」的孤儿对象。需要一个全局的
`gc` 子命令兜底。

分工：SysField 加 signal handler 处理可控中断（主）；sysbox 的 `gc` 兜底 SIGKILL 等
杀不掉的情况；当前版本把 provider 删除交给拥有凭据的 host supervisor，CLI 只做
受约束的只读预览。任何自动删除都必须经过统一 repository、活动引用、计划版本和
signed grant，不能由 CLI 本地 journal 直接执行。

## 二、孤儿识别（统一用 external ID 匹配）

`gc` 的预览流程是「收集持久引用 → 只读盘点 provider 对象 → 产生待核验候选」。

active 集合来自每个 topology 的 canonical lifecycle repository（`state.json.lifecycle`）：

| substrate | 外部对象来源 | 标识 | state 里的字段 |
|---|---|---|---|
| docker 容器 | Docker API + 完整归属标签 | container ID | `Resource.ExternalID` |
| docker 网络 | Docker API + 完整归属标签 | network ID | `Resource.ExternalID` |
| libvirt VM | `virsh list --all --uuid` + managed metadata | domain UUID | `Resource.ExternalID` |
| Firecracker VM | 经过路径、归属和配置摘要校验的 manifest | VM ID | `Resource.ExternalID` |

候选 = 「外部对象存在 + 归属证明完整 + canonical owner 已盘点 + 当前和保留
snapshot/journal 不引用」。候选不是删除授权；未知 owner、查询失败、活动执行和
generation/identity 不一致都必须跳过或让预览失败。

## 三、CLI 设计

```
sysbox gc [--runs DIR] --dry-run
```

- `--runs`：canonical runs 根目录，默认读取服务配置。
- `--dry-run`：只打印候选，不删除；这是当前唯一可用模式。

输出：每个候选至少包含类型和外部 ID；有 topology 显示字段时还输出 owner、
resource、generation 和 inventory digest。被跳过的外部对象输出原因。省略
`--dry-run` 明确拒绝；`--execute` 已删除，无兼容执行路径。

## 四、删除协议（尚未开放）

- CLI 不再直接调用上述 provider 删除命令。
- host supervisor 必须持有 provider 凭据，使用 API 签发的 command/grant，
  在 effect 前重验 inventory digest、owner/resource/generation、全部活动引用和
  plan 版本，完成后回报 signed stop receipt。
- interrupted/uncertain effect 只能 Observe/Recover，不能由 CLI 重放。

docker 网络要在容器之后删（容器还附着时网络删不掉）。

## 五、分期

**已完成切片**：canonical lifecycle 盘点、Docker/libvirt/Firecracker 预览候选、
state/snapshot/journal 引用摘要、GC grant 的 lifecycle identity fencing。

**后续**：API activity/placement/revision registry 汇合、正式 plan/authorized-delete
协议、host supervisor provider inventory 回调、TAP orphan inventory 和真实孤儿
回收验收；Firecracker manifest 盘点已经接入预览。

## 六、安全边界

- managed 标签或 title 单独不足以证明归属；必须核验完整持久身份。
- 不删除 state 或 provider 对象；历史旧格式和 standalone state 需要显式重建。
- 旧版本本地 GC journal 只作为诊断证据，不会被新版本解释为授权。
