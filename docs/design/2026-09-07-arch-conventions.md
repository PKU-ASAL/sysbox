# 架构约定

日期：2026-09-07

本文记录 sysbox 当前阶段几条刻意的架构约定/取舍，避免后人误读为缺陷。

## 一、变量 vs 秘密边界

sysbox 的 HCL 有两类「外部输入」，语义不同、求值时机不同，作者应区分：

| | `variable` + `var.<name>` | `secret://input/<name>` |
|---|---|---|
| 用途 | 构建期参数化（拓扑结构：count、image 名、网段等） | 运行期秘密（canary、flag 等） |
| 求值时机 | build 期（BuildEvalContext / BuildGraph 求值） | exec 期（executor 的 secret resolver 物化） |
| 明文去向 | 非敏感 input 明文可进 state/日志 | 明文只在执行期存在，不进 state/outputs/日志/审计 |

规则：

- **参数化拓扑结构** → 用 `variable` + `var.<name>`，经 apply 的 `inputs` 传值。
- **注入秘密** → 用 `secret://input/<name>`（直接写引用，不经过 `var`），经 apply 的
  `inputs` 传值，exec 期才物化。
- 不要用 `var.<name>` 承载秘密：那是把「运行期秘密」硬塞进「构建期参数化」，既
  违反 S3（明文进 state 的风险），也制造了求值时序耦合。

示例（推荐写法）：

```hcl
variable "team_index" { type = number }   # 参数化：每队网段

resource "sysbox_network" "lab" {
  cidr = cidrsubnet("10.200.0.0/16", 8, var.team_index)
}

resource "sysbox_node" "web" {
  provisioner "exec" {
    environment = {
      FLAG = "secret://input/flag"   # 秘密：直接引用，不经 var
    }
  }
}
```

## 二、state 格式不迁移

`state` 的 `SchemaVersion` 升级即破坏旧 payload，**不提供跨版本迁移**。加载旧版本
state 直接失败。

这是当前阶段的刻意取舍：sysbox 尚无生产存量数据，硬升级是最简单的正确行为。一旦
有存量 state，升级前需要单独设计迁移方案（按版本逐级迁移），不能静默丢弃。

## 三、单实例假设

apply 的并发合流依赖**进程内**机制：

- `lockTopology` 按 topology 串行化；
- 确定性 `operationKey` 派生出 run ID，`startWithResult` 用内存 `j.runs` 去重。

这套机制保证「同一 API 实例内」并发 apply 同一 revision+inputs 合流成一个 run。但它
**不跨实例**：多个 API server 共享一个 store 时，各自进程内的去重互不可见，会重复
dispatch。

当前 sysbox 是单实例部署，这是刻意的。若要横向扩展（多 API 实例），需要给 apply 补
store 级的 `RequestFingerprint` 去重（destroy 已具备，见 `startIdempotentDestroy` 的
`GetRunDispatch`/`CreateRunDispatch`），apply 尚未接入。
