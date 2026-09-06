# CIDR 内置函数 + var 预扫描修复 —— 设计

日期：2026-09-06
状态：已评审，待写实施计划

## 一、背景与动机

cyberfield 接真实 sysbox 端到端联调，暴露两个问题，都落在「HCL 求值」这一层：

1. **var 预扫描 bug（阻塞）**：`requiredCapabilitiesForTopology` 用无 inputs 的
   `config.BuildEvalContext` 求值含 `var.<name>` 引用的 resource，`var` 命名空间
   缺失，报「There is no variable named var」。apply 在把 run 派发给 agent 之前就
   死在这个「能力预扫描」上。

2. **CIDR 表达力缺失**：赛题作者只能用字符串拼接表示网段划分
   （`cidr = "${var.subnet_prefix}.0/24"`），脆弱、无类型安全、语义丢失。Terraform
   用类型化函数 `cidrsubnet`/`cidrhost` 表达同一意图，sysbox 应借鉴。

关注点分离不变：sysbox 只提供「拓扑编排」能力（含 CIDR 计算原语），比赛词汇
（team/flag/scenario）不进入 sysbox。

## 二、分阶段 roadmap

**阶段 1（本 spec，本次实施）**
1. 修 var 预扫描 bug（apply 预扫描传 inputs + 静态路径容错）。
2. 加 `cidrsubnet` / `cidrhost` 内置函数（IPv4，Terraform 兼容签名）。

**阶段 2（后续方向，本 spec 不实施）**
- 变量类型系统补全：`type` 约束从当前的 `number`/`string` 扩到 `list`/`map`/`object`。
- 更多类型化内置函数（按需：`lookup`/`concat`/`merge` 等）。

**阶段 3（远期，前置条件明确才做）**
- cty.Value + unknown 贯穿：把 decode 目标从 concrete struct 改成 cty.Value，
  让「值未知」在 plan/求值阶段自然传播。前置是 sysbox 引入独立的 plan/preview
  语义（当前只有「apply 即收敛」，收益有限）。

## 三、阶段 1 交付 1：修 var 预扫描 bug

### 根因

```
apply → StartApply → dispatchTopologyRun
  → requiredCapabilitiesForTopology(hclFile)
      → config.BuildEvalContext(root)     ← 无 inputs，var 命名空间缺失
      → decodeCapabilityResource 逐个求值 resource
      → cidr = "${var.subnet_prefix}.0/24" 求值失败 → 返回 error → apply 400
```

### 修复（两层，不用 unknown 占位）

1. **apply 路径传 inputs（主修）**：`requiredCapabilitiesForTopology(path string)`
   改为 `requiredCapabilitiesForTopology(path string, inputs map[string]string)`，
   内部改用 `config.BuildEvalContextWithInputs(root, filepath.Dir(path), inputs)`。
   apply 的 `dispatchTopologyRun` 传 `run.Inputs` —— 预扫描直接用真值，消灭「看不到
   inputs」这个假问题。

2. **静态路径容错（副修）**：supervisor 的 `maybeRepair`（仅在 `restart_on_crash`
   策略下调用）无 inputs，改传 `nil`。其 `err` 分支已 `Action = "restart_apply_failed"`
   并 finish run，本次补一条 `slog.Warn`（对齐「失败要 log，不静默吞」），自动修复
   是尽力而为、不因预扫描失败而中断 supervisor 本身。

### 不采用的方案

`buildEvalContextInner` 里对无 input 的 var 用 `cty.UnknownVal` 占位 —— 不采用。
因为 sysbox 的 `DecodeResource` 用 gohcl 解码到 concrete struct（`CIDR string`），
unknown 值赋给 concrete 字段会报错，占位法会在 decode 阶段撞墙。真正的对齐（阶段 3）
需要把 decode 目标改成 cty.Value，那是另一回事。

## 四、阶段 1 交付 2：`cidrsubnet` / `cidrhost` 内置函数

### 签名（严格对齐 Terraform）

```
cidrsubnet(prefix string, newbits number, netnum number) → string
cidrhost(prefix string, hostnum number) → string
```

### 语义

```hcl
cidrsubnet("10.200.0.0/16", 8, 0)   # "10.200.0.0/24"
cidrsubnet("10.200.0.0/16", 8, 1)   # "10.200.1.0/24"
cidrsubnet("10.200.0.0/16", 8, 99)  # "10.200.99.0/24"

cidrhost("10.200.0.0/24", 10)       # "10.200.0.10"
cidrhost("10.200.0.0/24", 0)        # "10.200.0.0"  （网络地址；不做「跳过网络/广播」偏移）
```

- `cidrsubnet`：`prefix` 先规范化（`netip.Prefix.Masked()`），`newbits` 是新增位数，
  `netnum` 是第几个子网（0 起）。
- `cidrhost`：`hostnum` 是主机部分数值（0 起），返回裸 IP（不带前缀）。

### 实现要点

- 复用 `net/netip`：`netip.ParsePrefix` 解析 → `Masked()` 规范化 → `Addr().As4()`
  做 4 字节位运算 → `netip.PrefixFrom`/`AddrFrom4` 拼回。sysbox 已大量用 `netip`，
  不造轮子。
- 注册点：`pkg/config/eval.go` 现有 `Functions` map（`localCtx`/`preCtx`/`ctx` 三处），
  与 `env`/`env_optional`/`toset` 同一套 `function.New` 机制，因此 `count`/`for_each`
  里也能用。
- 仅 IPv4；传入 IPv6 prefix 报错 `IPv6 not supported`（阶段 2 再议）。

### 错误处理（对齐 Terraform 报错风格）

| 场景 | 错误消息 |
|---|---|
| prefix 非法 | `invalid CIDR prefix` |
| `newbits` 使总长度超 32 | `prefix extension would exceed 32 bits` |
| `netnum` 超出 `2^newbits` | `subnet number out of range` |
| `hostnum` 超出主机位范围 | `host number out of range` |

## 五、测试要点

**交付 1（bug 修复）**
- 回归：HCL 含 `cidr = "${var.subnet_prefix}.0/24"`，apply 传真 inputs 时预扫描通过、
  run 正常下发。
- 静态路径：无 inputs 时 `requiredCapabilitiesForTopology` 失败被 log、不 panic。

**交付 2（函数）**
- 单元：`cidrsubnet` 正常划分（`/16→/24`、多个 netnum）；`newbits`/`netnum` 边界与越界；
  非法 prefix；IPv4 非规范地址（`10.1.2.3/16` → `10.1.0.0`）先 Masked。
- 单元：`cidrhost` 正常取 IP（hostnum 0/1/10）；`hostnum` 越界；非法 prefix。
- 集成：一个 HCL 用 `cidrsubnet` 派生 `sysbox_network.cidr` 能被 `DecodeResource`
  正确解码。

## 六、不做的事（YAGNI）

- 不做 IPv6（阶段 2 再议）。
- 不把 decode 目标改成 cty.Value（阶段 3）。
- 不主动改 cyberfield 现有场景的 HCL（`var.subnet_prefix` 拼接在 bug 修后能跑，是否
  迁到 `cidrsubnet` 由 cyberfield 侧决定）。
- 不加 `cidrnetmask` 等 Terraform 的其它 CIDR 函数（按需再加）。
