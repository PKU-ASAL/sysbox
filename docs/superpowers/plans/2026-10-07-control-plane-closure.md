# Sysbox control plane closure

**Goal:** 完成通用操作受理、可信证据与可验证执行；不引入下游业务编排。

**Architecture:** HTTP 只表达操作与证据；repository 在物理事务内裁决准入、轮次和状态转换；Agent/provider 按既有资源身份执行。缺失证据保持 unknown；不通过旧字段兼容、猜测或重放掩盖缺口。

**Constraints:** 在 refactor worktree 工作，不合并 main、不推送、不清理用户资源。API/Agent 同步升级；旧格式明确重建。多控制器资源端租约撤销独立于本轮，不宣称实现。Import adoption、自动 GC、snapshot/force-unlock 未实现的能力必须明确拒绝。

## Parallel ownership

- Admission：run_service/jobs/run_dispatch、claim handler 与 Agent claim。适配 main #43 产品语义，保留轮次 command ID 与 queued_at/revision 证明；原子 topology 准入；输入补交和终态重试。
- Evidence：cleanup inspection、capabilities、workspace 删除。读取 aggregate action journal、state、active Run，标明本地/远端证据边界；不把空 state 视为外部不存在。
- Storage：SQLite schema 与事件旧列回退、metadata 解码及准入存储约束。共享规则，不增加另一个状态机。
- Integration：执行 SPI 与不支持资源入口收口、真实全链路故障测试、文档与审查。

## Acceptance

- [x] 相同 key 相同请求得到原 run；参数改变冲突；终态不重执行；三后端并发准入一致。
- [x] API 重启后输入缺失先拒绝 claim，原请求补交恢复；旧轮次 claim 始终拒绝。
- [x] 空 state + unfinished action 不返回已清理；远端未观察不等于 absent；错误不伪装为空结果。
- [x] 旧 schema/缺完整记录明确重建；损坏 inventory/projection 不产生空成功。
- [x] 执行 SPI 对不支持的后台恢复/取消显式说明，前台 stdin 与文件确认不虚报成功。
- [x] 不支持的管理入口在副作用前拒绝，文档能力清单一致。
- [x] 真实 API→Agent→provider→repository 至少一条故障闭环，含丢失回报/重启及资源未重复创建证据。
- [x] 全量/race/真实 Postgres 与相关 provider 验收；独立审查后提交。

## 验证记录

2026-10-07：启用独立 PostgreSQL16 临时实例，`go test ./...` 全部通过；API/Agent/persistence/runtime/transport/state/guest-init 的 race 全部通过。vet、docs与diff检查通过。schema7覆盖并发新建、重开以及旧版/无版本/缺表拒绝且不修改旧库。

`test-remote-docker-fault-e2e` 通过：生产HTTP/WS Agent、独立API/Agent SQLite、真实Docker，丢弃已提交完成回执后API和Agent重建，重放仍为同一Run和唯一容器，旧claim/report拒绝，远端Observe返回同一UID/generation。限定同宿主共享不可变配置文件，不冒充跨宿主或OS SIGKILL验收。

真实Firecracker整套E2E通过（61.7秒），包含controller crash、reset代际隔离、跨进程恢复，以及新增stdin、SHA256文件往返、显式mode安装与目录目标拒绝。

交叉审查修复输入补交双读竞态、请求fingerprint不可变性、无revision预检快照、远端catalog误报gone、metadata删除与准入竞态、文件安装私有暂存权限与校验/同步。独立审查无未处理阻塞项。

保留边界：同代lease远端撤销、持久guest后台任务、ownership adoption、自动GC不在本轮实现；相关入口明确不支持。远端metadata普通删除缺少持久空状态回执时409，force只丢弃元数据且仍检查活跃生命周期与本地journal。
