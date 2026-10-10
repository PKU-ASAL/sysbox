# 显式 reset 续作

原 reset 被中断后，续作必须以原任务的完整范围为准。新 reset 不得隐式接管，
普通批次重试也不得补入遗漏节点。续作是单独的、需要授权的执行操作。

## 已实现：原子受理

`ResetContinuationRequest` 保存请求 ID、原 checkpoint 完整计划及摘要、拓扑 UID、
Run queued-at/revision、当前 state serial、已有 action 的完整版本快照，以及缺失
action 的 accepted intent。`CommitResetContinuation` 在同一 aggregate 事务内：

1. 校验原 Run、地址集合、计划摘要和每个 action 的不可变身份。
2. 拒绝其他 Run 的未完成 action，以及已改变的 state serial、action revision 或内容。
3. 拒绝 preparing、destroying 和未完成 applying 等未知 effect 阶段。
4. 保留已有 action，只补入缺失 intent，保存完整请求作为持久 receipt。

任何后续成员失败，整个事务回滚。一个原 Run 只能固定一份续作请求；精确重试
返回原 receipt，即使 action 进度或 state serial 已推进。读取 receipt 只能恢复
受理证据，不允许重放未知 provider effect。普通 intent 接口不得向已固定范围的
Run 追加成员。清空拓扑时同时清空 receipt。

文件、SQLite、Postgres 使用同一共享实现。新增的授权语义需要 aggregate format 5；
正常 repository 拒绝 format 4，不能让旧程序忽略 receipt 后覆盖它。

验收：三个后端（含真实 PostgreSQL 16）的 race 测试覆盖三节点补齐、第二个缺失
节点失败时全部回滚、未知阶段/版本/Run/计划拒绝、并发请求单胜者、serial 推进后
精确重试，以及 receipt 损坏和绕过追加成员的拒绝。runtime、Agent、API、CLI
完整 race 回归、vet 和独立审查通过。这些结果只证明受理事务，不证明 provider 恢复。

## 尚未接入：执行入口与旧现场导入

CLI/API 入口必须在读取证据前取得执行锁，验证原 checkpoint 的可信来源、真实
Run incarnation 和配置/baseline，再确认旧执行者及外部调用已经停止。原 action
缺少 queued-at/revision 时不能通过猜测补造：底层事务只检查字段一致性，不认证来源。
计划与 action desired digest 对应关系也必须由执行入口验证。

成功受理后，runtime 使用 receipt 中的固定集合，经已有阶段 CAS 推进；新执行日志
不得覆盖原 checkpoint。只读 recovery wrapper 不暴露续作写入能力。

保留的 format 4 异构故障现场仍未恢复。需要独立的一次性 verified import：
先保全原 aggregate/checkpoint/config 字节和摘要，验证旧执行者停止，再校验和导入
到 format 5；保留原 UID、serial、action ID、代际及 effect 证据。提交必须比较原始
内容，禁止直接修改 JSON 版本号。旧快照与维护 receipt 的处理必须逐项验证，不能
为了通过新格式校验丢弃历史证据。此导入工具和完整执行入口尚未实现。
