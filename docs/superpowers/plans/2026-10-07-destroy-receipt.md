# Destroy receipt and ordered architecture verification

**Goal:** owning Agent 成功销毁后，API 可依据持久回执安全删除 metadata；不再要求无条件 force。

**Architecture:** CleanupReceipt 绑定 topology/UID、Agent、RunID/queued_at/revision、attempt/lease owner、state serial 与观察时间。Agent 在本地 topology 锁内，从 repository 原子 state+journal snapshot 证明资源为零且无未完成 action。完成回报在当前 Run lease 事务内校验并保存 receipt；普通 SaveRun 不能授予该证明。receipt 不能从 projection、空 API state 或终态字符串合成。

**Invalidation:** placement 更换/重新创建、新 Run、新 NodeOperation、未知本地 journal 均使旧回执不能授权删除。删除继续持有单 API topology admission lock，保留 state serial/owner CAS。保证范围是遵循协议的单 API/owning Agent 控制面，不抵御独立绕过 API 的 writer。

## Sequence

- [x] 共享 receipt 形状与校验，三后端 completion 原子保存，terminal 重放严格比较，SaveRun 禁止伪造。
- [x] Agent 从原子 repository snapshot 生成证明，发送固定完成快照，失败不能出具回执。
- [x] metadata no-force 删除使用未被后续操作作废的最新 receipt；cleanup-inspection 公开来源和有效性。
- [x] 真实 HTTP→Agent→Docker Destroy→API 重启→no-force metadata DELETE；旧 receipt/new admission 拒绝。
- [x] 依次核查核心 repository、观察驱动引擎、Firecracker 恢复、幂等受理、旧格式拒绝，补齐本轮集成遗漏。
- [x] 全仓、race、真实 Postgres/KVM、独立审查与文档收尾。

不合并、不推送，不删除用户资源。测试只清理本次创建的对象。旧 completion 无证明可保存普通终态，但不能因此获得 metadata 删除权；不补造旧回执。

## 实施与验证记录

receipt使用共享completion事务保存，SaveRun/Restart/outbox不能授予证明。生成要求正state serial、持久topology UID、资源为零且无未完成journal。真实Docker测试分别丢弃Apply和Destroy完成的HTTP回执，API重建后重复回报幂等，最终正常metadata DELETE成功；不使用force或缓存projection。

按顺序核查后的增量修复：pause/resume先核对命令UID/generation再构造handle，持有topology锁到effect结束；删除Agent旧import直写state路径；缺snapshot的远端Run不回退可变workspace。12个代际/缺证明回归与旧import、旧快照拒绝测试通过。repository/API/Agent/runtime真实Postgres race通过；schema7与幂等合同保留。

真实Firecracker controller crash、reset、旧代拒绝、跨进程恢复通过。整套运行中曾一次文件安装失败；加入known/exit/stderr诊断后单次和连续8轮生命周期测试均通过，未再次复现，原因尚未确认。保留此限制，不将复测通过当作已定位修复。文件安装失败仍返回错误，不吞掉结果或重复effect。
