# 架构硬化 —— 设计

日期：2026-09-07
状态：已评审，待写实施计划

## 一、背景与目标

sysbox 端到端联调后，做了一次整体架构复盘，识别出 9 个潜在架构
问题。本设计覆盖其中 8 个「硬化类」问题（明确、低风险、可独立交付）；最根本的
`cty.Value + unknown 贯穿`（含变量类型系统补全）因前置「要不要 plan 语义」的方向
决策，单独拎出，不在本设计内。

## 二、范围

**本次做（8 项）：**
- 代码：#10 泄漏自动化防线、#6 projection 持久化、#3 函数注册去重、#9 配置校验。
- 文档/测试：#5 敏感 inputs 重启窗口、#2 var vs secret 边界、#7 state 迁移、#8 多实例。

**本次不做（单独决策）：**
- #1 `cty.Value` 贯穿 + #4 变量类型系统补全。

## 三、代码项设计

### #10 泄漏自动化防线

目标：加一个「canary 全局泄漏测试」，作为敏感值不泄漏的兜底防线。构造一次带
canary input 的 apply，遍历所有可持久化/序列化的产物，断言 canary 明文永不出现。

- 落点：`pkg/api/sensitive_input_leak_test.go`（或并入现有 `sensitive_input_persistence_test.go`）。
- 测试用固定 canary 字符串（如 `plaintext-canary-must-not-leak`）作为 input，跑通
  apply → dispatch → 写 state/health/command/checkpoint，然后从 store 读取所有
  持久化对象（runs、health snapshot、agent command、checkpoint、state），逐一把
  结构体 `json.Marshal` 后 `require.NotContains(canary)`。
- 现有 S3 测试是「点状」覆盖各 sink；本测试是「遍历 store 全集」的兜底，防止未来
  新增 sink 漏掉。

### #6 projection 持久化

目标：`ResourceProjection`（agent 上报的资源健康观察）当前只在内存 `agentRegistry`，
重启后丢失，导致 supervisor 在 agent 重新上报前把 health 写成 Unknown。改为持久化。

- store 接口新增 `SaveResourceProjection` / `LoadResourceProjection`（三后端 local
  json / postgres / sqlite，参照 `healthStore` 的 `SaveHealth`/`LoadHealth` 模式）。
- `handlePostAgentResourceProjection` 在写内存 `agentRegistry` 的同时持久化（双写；
  内存仅用于 SSE 流，store 是权威）。
- supervisor 的 `authoritativeTopologyHealth` 改为「从 store 读持久化 projection，
  回退 docker inspect」——重启后仍能用最后一次上报的健康。
- `LoadResourceProjection(topology)` 返回最近持久化的一份（按 `ObservedAt`）。

### #3 函数注册去重

目标：`Functions` map 在 `eval.go` 三处（`localCtx`/`preCtx`/`ctx`）重复，加函数
容易漏一处。

- 抽 `baseFunctions() map[string]function.Function`，返回 `env`/`env_optional`/
  `toset`/`cidrsubnet`/`cidrhost` 全部内置函数；三处 `Functions` map 统一由它构造。

### #9 配置校验

目标：state backend 配置一致性目前是隐式约定，缺失启动期校验。

- API 启动（`NewServer`/`newAPIStore`）时，若 `stateBackend` 为 postgres，校验其
  可达；不可达则启动报错。
- 文档化「agent 的 `BackendURL` 必须与 API 的 `stateBackend` 指向同一后端」——跨
  进程无法自动校验，靠配置约定。

## 四、文档/测试项设计

### #5 敏感 inputs 重启窗口

事实：`inputs` 是 transient，重启后 `markInterruptedRuns` 已把 `Assigned`/`Running`
的 in-flight run 标记为 `Failed` + `Recoverable`，不可再 claim——因此「重启后 claim
空 inputs」不会发生。缺的是文档化 + 测试锁定。

- 文档：在 `Run.Inputs` 字段注释处明确「inputs transient、重启即 fail in-flight
  run、secret 明文不跨重启存活」。
- 测试：构造 `Assigned`/`Running` 的 run，跑 `markInterruptedRuns`，断言变 `Failed`
  且不可 claim。

### #2 var vs secret 边界

- 文档：明确 `var.<name>`（构建期参数化，build 期求值）与 `secret://input/<name>`
  （运行期秘密，exec 期物化）的选用边界。落点 `docs/design/` 一篇短文档。

### #7 state 迁移

- 文档：明确「state 格式不迁移，`SchemaVersion` 升级即破坏旧 state」。当前无生产
  数据，硬升级是合理取舍。

### #8 多实例

- 文档：明确「apply 合流是单实例假设（内存锁 + 确定性 operationKey），多实例需
  store 级 `RequestFingerprint` 去重（destroy 已有、apply 没有）」。当前单实例部署。

## 五、测试要点

- #10：canary 泄漏测试遍历 store 全集，断言 canary 不出现。
- #6：projection 持久化后，重启（重建 server）仍能 `LoadResourceProjection` 读回。
- #3：三处 `Functions` 都含 `cidrsubnet`/`cidrhost`（现有测试已覆盖，去重后仍过）。
- #5：`markInterruptedRuns` 把 in-flight run 标记 failed 的回归测试。
- #9：postgres 不可达时启动报错（若可测）；否则只文档。

## 六、不做的事

- 不做 `cty.Value` 贯穿 + 变量类型系统补全（单独决策）。
- 不做 state 迁移框架、多实例 store 级去重（YAGNI，见 #7/#8）。
- 不重构 eval context 的整体结构（只做 #3 的函数去重，不碰 buildEvalContextInner
  的其余逻辑）。
