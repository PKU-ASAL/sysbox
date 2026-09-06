# 全局 revision 与 upsert apply —— 设计

日期：2026-09-05
前置：`docs/design/topology-readiness-contract.md`（S1–S3 已实现）
消费者契约：`cyberfield/api/sysbox.v1.yaml`

## 一、背景与目标

cyberfield（比赛编排）需要一个「一步收敛」的接口：`POST /v1/topologies/{name}/apply`
以 revision digest + sensitive inputs 收敛一个拓扑，拓扑不存在时创建、存在时收敛。
今天的多步流程（create → hcl → plans → apply(plan_id) → 轮询 run）要收进
apply 与 conditions 背后。

关注点分离是本次设计的硬约束：

- cyberfield 管比赛（campaign / scenario / team / assignment）
- CTFd 管评分（flag / submission / score）
- sysbox 管拓扑（topology / revision / inputs / conditions / outputs）

sysbox 侧不出现任何比赛词汇；cyberfield 的概念到 sysbox 概念的映射只发生在
cyberfield 的 adapter 层。

## 二、架构定位：一套核心 + 两个前端 + 两种存储

- **一套核心**：decoder / planner / executor / state-manager / provider，CLI 与
  API/Agent 共享（现状已成立）。
- **两个前端**：CLI（本地直跑，HCL 从磁盘读、state 写本地后端）与 API（服务端，
  HCL 从 revision registry 读、state 写服务端后端）。执行语义同一，仅 I/O 源不同。
- **两种存储**：
  1. **内容存储（revision registry）**：全局、内容寻址的 HCL blob，API-only。
     CLI 不需要（人手里有 HCL 文件）。
  2. **拓扑状态（topology state）**：唯一的「实际存在哪些资源」，CLI 与 API 共用
     state-manager 代码，只是落地后端不同。

revision registry 是**新增的一种存储**，不是「第二套拓扑状态」。

## 三、端点变更

### 新增

```
POST /v1/revisions
  请求 { "hcl": "<完整拓扑 HCL>" }
  响应 201 { "revision": "sha256:<hex>" }
```

- digest = HCL 字节的 SHA256，内容寻址、幂等（同一 HCL 重复 publish 返回同一 digest）。
- 只存内容，不绑定任何 topology；一份赛题 HCL 被多支队伍共用。

### 扩展

```
POST /v1/topologies/{name}/apply
  请求 { "revision": "sha256:<hex>", "inputs": {"flag": "..."}, "deadline_at": "..." }
  响应 202 { "name": "...", "status": {...} }   // 与 GET 同形
```

- **upsert**：拓扑不存在用 revision 的 HCL 建；存在则收敛到该 revision。
- `inputs` 是 sensitive 值（S3：只存引用不存明文）。
- `deadline_at` 是消费者指定的本次收敛截止时刻，覆盖 sysbox 配置默认。
- `202` 承诺 desired 已持久化（run 记录已落库，agent 崩溃后可重放）。
- 之后消费者只读 `GET /v1/topologies/{name}` 的 conditions。

### 删除

- `POST /v1/topologies`（创建 workspace，被「publish + upsert apply」取代）
- `PUT /v1/topologies/{t}/hcl`（改 HCL，被「publish 新 revision + apply」取代）
- `POST /v1/topologies/{t}/plans`（plan 是内部实现细节，不再暴露）

「创建拓扑」的职责并入 apply 的 create-if-not-exists，不是砍功能。

### 保留

- `GET /v1/topologies`（列表）、`GET /v1/topologies/{t}`（状态）、
  `POST .../destroy`、`POST .../reset`、`GET /v1/runs/{id}`（只读，供 CLI/调试）。

## 四、revision registry 数据模型

- 不可变、内容寻址：`digest = sha256(hcl)`。
- 记录：`{revision(digest), hcl, created_at}`。
- 幂等：同一 HCL 重复 publish 不产生新记录。
- 生命周期：不随 topology 删除而删除（一份 HCL 被多队共用）；GC 策略后定，初期不删。

## 五、并发与幂等语义

1. **并发 apply 同一 revision+inputs → 合流成一个 run**：内容寻址 revision + inputs
   拼出确定性指纹，复用现有 `OperationKey` + `RequestFingerprint` 幂等机制。
2. **收敛中新 revision → 排队，不抢占**：沿用 `lockTopology` 串行化，后写覆盖靠
   排队自然后写实现。reconciler 不 care 抢占。
3. **202 = desired 已持久化**：run 记录（revision + inputs + deadline）落库后才返回。
4. **repair / reset / destroy / allow_unsafe_state 不动**：契约只重定义 apply。

## 六、状态补全（读侧，独立于写侧）

`GET /v1/topologies/{name}` 的 `status` 补全：

- **outputs**（急）：从 state + eval context 求值 HCL 的 `output` 块。敏感值脱敏
  （S3：引用不泄漏）。gateway 据此开通访问。
- **nodes**（不急）：从 state 的 `sysbox_node` 资源派生 name / address(primary_ip) /
  state 原文。
- **deadline_at**（写侧）：apply 入参，supervisor 到点判失败。

## 七、实施顺序（先加新、后删旧）

1. 加 `POST /v1/revisions` + 扩展 `POST /apply`（upsert），旧端点暂留。
2. 补 `status.nodes` / `status.outputs` / per-apply `deadline_at`。
3. 删旧端点（create / hcl / plans）+ 更新 `controlplane_test.go`。

分步保证新路径可回退。

## 八、测试要点

- revision：内容寻址幂等、同一 HCL 同一 digest。
- apply upsert：不存在则建、存在则收敛；同 revision+inputs 幂等合流。
- 并发：并发 apply 同 revision 只产生一个 run。
- sensitive：outputs 里敏感值不泄漏（沿用 S3 的 canary 测试模式）。
- 删除后：旧端点 404，`controlplane_test.go` 相应更新。

## 九、不做的事

- 不实现 `sysbox up` 命令（独立自检工具，后续）。
- 不改 destroy / reset / repair 的语义。
- 不做 revision 的 GC（初期不删）。
