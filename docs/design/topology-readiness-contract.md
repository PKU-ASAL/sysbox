# 拓扑就绪契约：conditions、check/assert 与 sensitive 输入

日期：2026-09-04
状态：待评审
适用范围：Sysbox 本体。本文不引入任何来自消费者领域的概念。

## 一、要解决的问题

Sysbox 今天能回答「拓扑装配到了什么程度」，但答案分散在四个端点里，且**没有任何端点回答「这张拓扑现在可以交给使用者了吗」**。

消费者要判断就绪，今天必须自己拼：

| 想知道 | 今天要问 |
|---|---|
| 资源是否都在、有无漂移 | `GET /v1/topologies/{t}/health` |
| desired 与 state 的差异 | `GET /v1/topologies/{t}/stack-state` |
| 拓扑导出了什么 | `GET /v1/topologies/{t}/outputs` |
| 单个资源的细节 | `GET /v1/topologies/{t}/resources` |
| 某次 apply 结束了没 | `GET /v1/runs/{id}`（轮询） |

**没有 `GET /v1/topologies/{name}`。** 单个拓扑无法被整体读取。

后果不是不便，是把编排责任推给了每一个消费者：每个人都要自己实现「四次调用 + 轮询 run + 拼装状态机」，而这段逻辑一旦写错，表现就是环境看起来好了但实际没好。这类误判在真实使用中已经发生过——一个声明 10 秒超时的服务检查实际跑了 133.5 秒，而消费者拿到的错误是自己轮询预算耗尽，不含任何关于失败原因的信息。

更根本的一条：**Sysbox 今天不拒绝无限收敛。** apply 之后卡住多久都不算错，没有任何一层会到点。有界性只能由每个消费者各自实现，于是实际上没人实现。

## 二、三项改动

### S1 · 单个拓扑可整体读取，并报告 conditions

新增 `GET /v1/topologies/{topology}`：

```jsonc
{
  "name": "lab-a",
  "status": {
    "observed_revision": "sha256:…",
    "phase": "converging",              // converging | ready | failed | destroying | gone
    "deadline_at": "2026-09-04T02:10:00Z",
    "conditions": [
      {"type": "Applied",     "status": "True",  "observed_at": "…"},
      {"type": "Provisioned", "status": "True",  "observed_at": "…"},
      {"type": "Asserted",    "status": "False", "observed_at": "…",
       "reason": "CheckFailed",
       "message": "edge 不应能连到 core:5432，但连上了",
       "failed_checks": ["edge_cannot_reach_core"]},
      {"type": "Ready",       "status": "False", "reason": "AssertionFailed"}
    ],
    "nodes":   [{"name": "edge", "address": "10.200.3.4", "state": "running"}],
    "outputs": { }
  }
}
```

`status` 取 `"True"` / `"False"` / `"Unknown"`（Kubernetes 约定）。

**`Unknown` 与 `False` 必须区分。** `Unknown` 表示尚未评估，`False` 表示已评估且失败。二者混同会让消费者在收敛窗口内把「检查还没跑」当成终态失败——每一次 apply 都会先经过这个窗口，所以这不是边缘情况，是必然路径。

`conditions[]` 不是新的事实来源，是已有事实的组合投影：`Applied` 来自 run 的终态，`Provisioned` 来自 `TopologyHealth.Status`，`Asserted` 来自 S2 的 check 求值结果。**不新增可写状态。**

#### 两个决定

**(a) `deadline_at` 属于 Sysbox，不只属于消费者。**

Sysbox 自身拒绝无限收敛：到点即 `phase=failed`，并写明原因。这样每个消费者免费获得有界行为，而不是各自实现一遍——后者的实测结果是没人实现。

**(b) Sysbox 只报告，不强制。**

`Ready = Applied ∧ Provisioned ∧ Asserted`，是计算出的组合条件，**不是闸门**。断言失败时 Sysbox 照常交付拓扑。门禁是消费者策略：

- 办比赛的消费者门禁于 `Ready`——断言不过不发凭证
- 调试者门禁于 `Applied`——断言失败不影响工作
- 巡检工具订阅 `Asserted` 的翻转

这与 Terraform 的 `check` 产生警告而非错误是同一个选择：报告者不替使用者决定什么算可用。

#### 与现有 `Checks` 的关系（必须写明，否则会被合并掉）

`controlplane.ResourceHealth` 已有 `Checks map[string]ResourceCheckHealth{OK, Reason}`。它与本文的 `check` 块**不是同一件事**，不得合并：

| | `ResourceHealth.Checks`（已有） | `check` 块（S2 新增） |
|---|---|---|
| 谁写的 | provider 实现 | 拓扑作者 |
| 问什么 | 这个资源本身健康吗（进程在跑吗、地址分配了吗） | 这张拓扑符合作者的意图吗（A 不该能连到 B） |
| 粒度 | 单个资源 | 整张拓扑，可跨资源 |
| 失败含义 | 基础设施有问题 | 拓扑起来了，但不是作者要的那张 |
| 落到哪个 condition | `Provisioned` | `Asserted` |

一个节点可以完全健康（`Checks` 全 OK）而拓扑断言失败——那正是「环境起来了但隔离策略写错了」，是最需要被区分出来的一种失败。

### S2 · `check` / `assert`（对齐 Terraform）

沿用 Terraform 1.5 的 `check` 块语义，逐字复用其词汇。

```hcl
check "portal_http" {
  data "sysbox_exec" "health" {
    node = sysbox_node.portal
    argv = ["/usr/bin/curl", "-fsS", "--max-time", "5", "http://127.0.0.1:8080/health"]
  }

  assert {
    condition     = data.sysbox_exec.health.exit_code == 0
    error_message = "portal /health 未就绪：exit=${data.sysbox_exec.health.exit_code}"
  }
}

check "edge_cannot_reach_core" {
  data "sysbox_reach" "edge_core" {
    from = sysbox_node.edge
    to   = sysbox_node.core
    port = 5432
  }

  assert {
    condition     = data.sysbox_reach.edge_core.reachable == false
    error_message = "edge 不应能连到 core:5432，但连上了"
  }
}
```

新增项只有三个：

| 新增 | 说明 |
|---|---|
| `check "NAME" {}` | 唯一的新顶级块，纯容器；内含至多一个 scoped `data` 与一个或多个 `assert` |
| `data "sysbox_exec"` | 在节点内执行 argv，导出 `exit_code` / `stdout` / `stderr` / `truncated` |
| `data "sysbox_reach"` | 从 `from` 节点探测 `to` 节点的 `port`，导出 `reachable` |

`assert` / `condition` / `error_message` / 失败产生警告而非错误 / 在 apply 末尾求值——全部照抄，零偏离。`condition` 是任意 HCL 布尔表达式，`&&`、`||`、`contains()`、`can()` 由 HCL 免费继承。**不发明断言 DSL。**

#### 三处必须偏离 Terraform

1. **check 结果必须可通过 API 读取。** Terraform 的 check 结果只出现在 CLI 输出与 HCP 里。Sysbox 必须把它落进 `status.conditions[]` 的 `Asserted`——这是消费者唯一的读取途径，属刚需，不是增强。
2. **探测位置在节点内部。** Terraform 的 `data "http"` 从 Terraform 运行处探测；`sysbox_exec` 从节点内部探测。这不是词汇偏离，是通过 data source（Terraform 自身设计的扩展点）做的能力扩展。**负向隔离断言（「A 必须不能到达 B」）只能从节点内部验证**，这是本能力的主要动机。
3. **check 不进 apply 依赖图。** 与 Terraform 一致：check 是叶子，不可被其他资源引用，仅在 apply 之后求值，不参与编排排序。这同时是保持引擎轻薄的关键——否则要处理 check ↔ resource 的环。

#### 扩展点现状（2026-09-04 实测核对）

本次改造在 Sysbox 侧是**实现缺口，不是架构缺口**。所需扩展点全部已存在：

| 需要 | 位置 | 现状 |
|---|---|---|
| 顶级块注册 | `pkg/config/schema.go` 的 `Root` | 已有 7 种块；加 `Checks []CheckBlock` 是一行 |
| data source 处理器接口 | `DecodeData(config.DataBlock, *hcl.EvalContext)` | 已有，先例见 `pkg/runtime/resource_image.go:146` |
| data 块入图 | `expandDataBlock`，`pkg/runtime/workspace.go:256` | 已有 |
| 真实 `hcl.EvalContext`（locals / module outputs / count） | `pkg/config/eval.go` | 已有 |
| 节点内执行 | guest-exec 已是一等 API | 已有 |
| 拓扑图查地址（`sysbox_reach` 需要） | `pkg/graph` | 已有 |

`DataBlock` 已是 `{Type, Name, Remain hcl.Body}` 的通用形状，两个新 data source 无需改动 schema 本身。

**代码预算**（沿用 2026-09-01 估计，扩展点核对后仍成立）：

| 改动 | 位置 | 估计 |
|---|---|---|
| `Checks []CheckBlock` 加入 `Root` | `pkg/config/schema.go` | ~20 行 |
| `sysbox_exec` 的 `DecodeData` 与求值 | 照 `DataImageResourceHandler` | ~90 行 |
| `sysbox_reach` 的 `DecodeData` 与求值 | 同上 + 查 `pkg/graph` 取地址 | ~110 行 |
| apply 末尾求值 `condition`，落 `Asserted` | `pkg/runtime` | ~120 行 |
| 组合 conditions 并挂到新端点 | `pkg/api/handler_topo.go` | ~60 行 |

合计约 400 行，零新依赖，零新外部概念。`sysbox_exec` 是 guest-exec 的薄包装；`sysbox_reach` 是 guest-exec 加图查询的组合。

> **这个数字是低估**（2026-09-04 复核后补）。它只覆盖 S1 的组合逻辑与 S2 的四块，漏了三项：
>
> 1. **`deadline_at` 的强制执行。** 报告一个字段是几十行；让 apply/run 真的到点中止要动执行引擎，且必须决定「中止」的语义——是标记失败就停手，还是回滚已创建的资源。后者牵涉删除权，代价高得多。
> 2. **S3 的执行与泄漏测试。** `sensitive` 的解析确实便宜（`VariableBlock` 已有），但「不得出现在 state / outputs / 日志 / 审计 / plan diff」是五处强制加五个测试。其中 plan diff 最容易漏：要显示「这个值变了」而不能显示变成了什么。
> 3. **`apply` 改 upsert、把 plan/run 收进背后**（见第五节）。这是**改既有接口形态**，不是新增，可能是整批里最大的一块。今天是 create → hcl → plans → apply(plan_id) → 轮询 run → health 六步，要收成一步。
>
> 真实量级更可能是 400 行的两到三倍。先做哪一项见第五节末的顺序建议。

**命名**与已有资源一致（`sysbox_node` / `sysbox_image` / `sysbox_network`），取 `sysbox_exec` / `sysbox_reach`；`exec` 同时与已有的 `provisioner "exec"` 对齐。

#### 前置修复：guest-exec 的时延必须有界

`check` 的可用性完全依赖 guest-exec 的超时真的生效。已知两处会让它失效，其中一处已修：

| 项 | 状态 |
|---|---|
| `provider/docker/exec.go` 的 `StdCopy` 在劫持连接上裸读，ctx 无法打断，`timeout_seconds` 对 docker 驱动完全不生效 | ✅ 已修，PR #9（抽出 `readExecStreams`，ctx 结束即关连接） |
| `pkg/agentexec/agent.go` 的 `guest_execution` 分支中，`ReportGuestExecutionStart` → `StateManager` → `mgr.Load()` 全在 `timeout_seconds` 生效之前（`agent.go:308-317`）；而 `pkg/state/manager.go` 的 `Load()`（:94）**与 `Save()`（:139）** 都写死 `context.Background()`，取消传不进去 | ❌ 未修 |
| 关闭劫持连接只解开调用方，被 exec 的进程仍留在容器里（实测客户端断开后 `sleep 600` 仍存活） | ⚠️ 需在 `sysbox_exec` 层面处理：argv 自身应带自限超时，或 exec 结束时清理残留进程 |

第二项属同一类缺陷（时延无界），且 `check` 会把 guest-exec 的调用频率显著提高——不修等于把同一个挂死放大。**它是 S2 的前置条件，不是后续优化。** 注意是**两处**写死（`Load` 与 `Save`），2026-09-04 实测确认；早期记录只提到 `Load`。

### S3 · `sensitive` 变量（只写输入）

```hcl
variable "flag" {
  type      = string
  sensitive = true    // → 不入 state、不入 outputs、不进日志、不进审计
}
```

```
POST /v1/topologies/{topology}/apply   {"revision": "…", "inputs": {"flag": "…"}}
```

明文只存在于请求体与 run 期间，用完即忘。

`sensitive` 沿用 Terraform 词汇。Sysbox 不知道这个值是什么——是 flag、密码还是许可证密钥，由使用者决定。

#### 现状比预期更近

`VariableBlock` **已经存在**（`pkg/config/schema.go`），且是 `{Name string, Remain hcl.Body}`——`Remain` 已接受任意属性，所以 `sensitive = true` **今天就能解析**。缺的是解码与执行，不是块定义。

`pkg/secret/reference.go` 已有可插拔的引用机制：`Reference{Source, Name}`，`secret://env/NAME` 与 `EnvironmentResolver` 是现成先例。新增 `secret://input/<key>` 是加一个 source 与一个 resolver。

`pkg/controlplane` 的 `GuestFilePut` 已只携带 digest 与 fetch 引用、不含字节——这是正确模式的既有样板。

#### 硬约束

sensitive 变量的值不得出现在以下任何位置，**每一处都必须有自动化测试，不得仅以注释表达**：

- Postgres state backend（`pkg/state/backend_postgres.go`）
- 拓扑 outputs
- run 日志
- 审计与 action log
- plan 的 diff 输出（`pkg/value.Diff` 的结果）

最后一项容易漏：plan 要显示「这个值变了」而不能显示变成了什么。

### S4 · 装配复用已有 provisioner，不新造契约

`provisioner "exec"` / `provisioner "file"` 已存在（`pkg/config/schema.go`）。装配不引入新契约。

Ansible 作为**可选的 provisioner 实现**，以独立镜像发布，不进 Sysbox 主镜像——保持更通用组件的安装面轻薄。

## 三、明确不做

| 不做 | 理由 |
|---|---|
| 引入任何消费者领域的概念 | Sysbox 的词汇止于 project / workspace / revision / plan / run / topology / node / network / image / provisioner / data / check / assert / agent / capability / lease。比赛、队伍、赛题、评分不属于这里，一旦引入不可逆 |
| 把 `Ready` 变成闸门 | 产品要服务多种消费者，门禁策略属消费者（见 S1(b)） |
| 发明断言 DSL | `condition` 用 HCL 布尔表达式即可；更复杂的逻辑写进 `sysbox_exec` 调用的脚本 |
| 让 check 参与依赖图 | 会引入 check ↔ resource 的环，且偏离 Terraform |
| 把 `ResourceHealth.Checks` 与 `check` 块合并 | 两者的作者、粒度与失败含义都不同（见 S1 的对照表） |
| 冻结 conditions 的 OpenAPI | 待第一个真实消费者跑通后再冻结。先写规范再发现不好用，代价更大 |

## 四、验收

| 项 | 判据 |
|---|---|
| S1 | `GET /v1/topologies/{name}` 返回 conditions；`Unknown` 与 `False` 在收敛窗口内可区分；到 `deadline_at` 后 `phase` 变 `failed` 且带原因 |
| S2 | 对一个故意写坏的 project，`Asserted=False` 且 `message` 是**作者自写的 `error_message` 原文**；断言失败时拓扑照常交付（不是闸门） |
| S2 前置 | 声明 `timeout_seconds: 10` 的 exec 在 10 秒左右返回，误差有界；`agentexec` 的取消路径可打断 |
| S3 | sensitive 值不出现在 state backend / outputs / run 日志 / 审计 / plan diff 的**自动化测试**通过，此项为阻塞性验收 |
| 词汇纪律 | 一个断言 HCL 与 API 响应中不出现消费者领域词汇的测试 |
| 独立可用 | `sysbox up ./project` 成为完整的自检工具（起 + 装配 + 断言），不依赖任何特定消费者 |

## 五、与消费者的协同

第一个真实消费者已把它对本契约的要求写成可执行的文件：

```
cyberfield/api/sysbox.v1.yaml
```

其中标 `x-cyberfield-status: pending` 的项正是 S1–S3。该文件同时声明了两条本文应满足的语义：

- **`apply` 是 upsert**：拓扑不存在时创建，存在时收敛到指定 revision。消费者不做「先查存在、不存在则创建、再 apply」——那会在多个消费者实例之间制造竞态。
- **plan 与 run 是 Sysbox 的实现细节**：消费者不感知 plan 资源、不轮询 run。apply 同步受理后异步收敛，此后只读 conditions。

这两条是本次改动对现有接口形态的实质要求：今天的多步流程（create → hcl → plans → apply(plan_id) → 轮询 run → health）要收进 apply 与 conditions 背后。

让消费者先写契约、Sysbox 再实现，是刻意的顺序——先写规范再发现不好用，返工更贵。

### 推进顺序建议

```
0.  agentexec / state 的取消路径（S2 前置，两处 context.Background()）
1.  apply 改 upsert，plan/run 收进背后        ← 先做这个
2.  S1 conditions + 新端点
3.  S1 deadline_at 的强制执行
4.  S2 check / assert / sysbox_exec / sysbox_reach
5.  S3 sensitive + 五处泄漏测试
```

**为什么 `apply` 改 upsert 排在最前**：它决定其余各项挂在什么形状的接口上。conditions 挂在一个仍需先建 plan 再 apply 的流程上，与挂在一步 upsert 上，是两套不同的状态推导——形状定错了，S1/S2/S3 都要跟着返工，而它们合计是这批里的大头。

**为什么第 0 项在第 1 项之前**：它是纯 bug 修复，与接口形状无关，可独立合并；而 `check` 会显著提高 guest-exec 的调用频率，带着一条已知会挂死的路径去做 S2，只会把问题放大到更难定位。

## 六、来源

本文的 S1–S4 取自 2026-09-01 的跨仓库架构决策文档（现存于 cyberfield 仓库历史：`git show fe18792:docs/architecture/sysbox-boundary-design.md`），并在 2026-09-04 对 Sysbox 现状逐条重新核对。核对结果修正了三处：

1. **`VariableBlock` 已存在**，`sensitive` 是加属性而非加块——比原估计更小。
2. **不存在 `GET /v1/topologies/{name}`**，原文档说的「挂到已有 `/health` 端点」不成立；单个拓扑今天无法整体读取，需新增端点。
3. **`ResourceHealth.Checks` 已存在**，原文档未提及。它与 `check` 块的区分必须写明，否则会被后人合并，丢掉「基础设施有问题」与「拓扑不是作者要的那张」这两种失败的区别。
