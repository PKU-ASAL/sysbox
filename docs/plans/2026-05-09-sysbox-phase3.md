# sysbox Phase 3 — Hook-based IoC Extraction & Prediction Matcher

> **Goal:** 让 sysbox 成为 RL 训练的评分系统，同时对 agent 完全透明。Agent 直接与靶场节点交互（SSH/HTTP/exploit），不感知 sysbox。sysbox 通过 **hook 机制**拦截 agent 的工具调用，用专有 IoC 提取模型翻译成预期的系统调用模式，在全量 tracee 事件流里验证，输出 `match_report.json` 作为 RL reward。

---

## 设计动机

### 为什么不让 agent 直接输出 syscall 预测

Phase 3 初稿让 agent 在每步前声明结构化 syscall 预测（`{"event": "execve", "args": {...}}`）。这个方案有两个问题：

1. **污染 agent 的思考过程**：agent 在思考"如何利用 CVE-XXXX"时还要预测 `execve` 的 `pathname`，两种认知层次混在一起，干扰渗透效果
2. **无法泛化到 novel exploits**：手写规则只覆盖 nmap/nc 等已知工具

### 核心设计原则

```
Agent 的职责：只关注攻击策略，像真实红队一样思考和行动
sysbox 的职责：透明地观测和评分，对 agent 完全不可见
IoC 提取模型的职责：把 agent 的工具调用翻译成可验证的内核行为预期
```

**sysbox 对 agent 完全透明**：agent 只看到普通 Linux 机器，通过 SSH/HTTP 与节点交互，不经过也不感知 sysbox API。

---

## 整体架构

```
┌──────────────────────────────────────────────────────┐
│                     Agent                            │
│  Think: "扫描内网找开放服务"                          │
│  Act:   bash_exec("nmap -p 22,80 10.0.1.0/24")      │
│  （agent 不感知 sysbox，直接与节点 SSH/HTTP 交互）    │
└───────────────────────┬──────────────────────────────┘
                        │ tool call 拦截（pre-execution）
┌───────────────────────▼──────────────────────────────┐
│                   Hook Layer                         │
│  pkg/hook/                                           │
│  ├─ 拦截 tool call（tool name + command + context）  │
│  ├─ → IoC Extractor（规则 + 小模型，<100ms）         │
│  ├─ → 写入 predictions.jsonl                        │
│  └─ 执行原始 tool call（不修改）                    │
└──────────────────┬────────────────────────────────────┘
                   │
       ┌───────────┴───────────┐
       │                       │
┌──────▼──────┐        ┌───────▼──────┐
│ IoC Extractor│        │ tracee 事件流 │
│             │        │ events.jsonl  │
│ tool_call → │        │（全量系统调用）│
│  syscall    │        │ 全局 scope    │
│  patterns   │        │ 无 container  │
└──────┬───────┘        │ scope filter │
       │                └───────┬──────┘
       │  predictions.jsonl     │  events.jsonl
       └───────────┬────────────┘
                   │
          ┌────────▼─────────┐
          │  Prediction Matcher│
          │  + IoC Rule Engine │
          └────────┬──────────┘
                   │
          match_report.json
          ├─ prediction_hit_rate    → 主要 reward 分量
          ├─ unscripted_iocs        → 负向信号（做了没说）
          └─ ttp_coverage           → 额外奖励
```

---

## 核心约束（Phase 3 方案 B 定版）

1. **Agent 完全隔离**：agent 的 context 不含任何 syscall 格式或 sysbox 相关内容
2. **Hook 透明**：tool executor 被 wrap，agent 感知不到差异
3. **IoC 提取模型独立训练**：match_report 作为自监督信号，构建数据飞轮
4. **Matcher 吃全量事件**：tracee 无 container scope filter，Labeler 不再设置 session_id

---

## Event Schema（Phase 3 更新）

去掉 `session_id` 作为主要归因字段（保留为可选外部 trace 关联），由 Matcher 填充归因信息：

```go
// pkg/sensor/sensor.go（更新）
type Event struct {
    // 原始观测（不变）
    NodeID    string         `json:"node_id"`
    CgroupID  uint64         `json:"cgroup_id"`   // 元数据，不用于归因
    Timestamp int64          `json:"ts"`
    PID       int            `json:"pid"`
    PPID      int            `json:"ppid"`
    Type      string         `json:"type"`
    Name      string         `json:"name"`
    Args      map[string]any `json:"args"`

    // 由 Matcher 填充（raw events.jsonl 不含这些字段）
    // 仅出现在 annotated_events.jsonl 里
    MatchedPrediction bool   `json:"matched_prediction,omitempty"`
    AgentStep         int    `json:"agent_step,omitempty"`
    TTP               string `json:"ttp,omitempty"`    // MITRE ATT&CK ID
    IoC               string `json:"ioc,omitempty"`    // IoC rule ID
    IsAttack          bool   `json:"is_attack"`        // matched_prediction OR ioc != ""
}
```

**原则**：`events.jsonl` 是原始干净数据（只有 tracee 观测）；`annotated_events.jsonl` 是 Matcher 运行后的语义增强版。两者分开存储，互不污染。

---

## Hook Layer

### 接口设计

```go
// pkg/hook/hook.go
type ToolCall struct {
    ToolName  string    `json:"tool_name"`   // "bash_exec", "ssh_exec", "http_request"
    Command   string    `json:"command"`     // 实际执行内容
    Context   string    `json:"context"`     // agent 的 chain-of-thought（可选）
    Node      string    `json:"node"`        // 目标节点（从工具参数解析）
    RunID     string    `json:"run_id"`      // Langfuse run ID
    AgentStep int       `json:"agent_step"`
    Timestamp time.Time `json:"timestamp"`
}

// Hook 包装 tool executor，对 agent 透明。
type Hook struct {
    Extractor  Extractor
    PredWriter *PredictionWriter
}

func (h *Hook) Wrap(call ToolCall, executeFn func() error) error {
    // 1. 提取 IoC，生成 Prediction
    pred := h.Extractor.Extract(call)
    h.PredWriter.Write(pred)
    // 2. 执行原始工具调用（不修改）
    return executeFn()
}
```

### IoC Extractor

```go
// pkg/hook/extractor.go

// Extractor 把 ToolCall 翻译成 Prediction（预期 syscall 模式）。
// Phase 3.0: 纯规则（覆盖常见攻击工具）
// Phase 3.1: 规则 + 小模型（Qwen-7B fine-tune）
// Phase 3.2: 模型用 match_report 自监督 fine-tune
type Extractor interface {
    Extract(call ToolCall) Prediction
}

// RuleExtractor 是 Phase 3.0 的实现。
// 规则存储在 rules/*.yaml，匹配 tool_name + command 关键词。
type RuleExtractor struct {
    rules []ExtractionRule
}
```

**规则格式（`rules/extraction/`）：**

```yaml
# rules/extraction/network_scan.yaml
- id: "ext-nmap"
  match:
    command_contains: ["nmap"]
  predict:
    - event: execve
      args: {pathname: "/usr/bin/nmap"}
    - event: connect
      args: {}
  ttp: "T1595.001"

- id: "ext-masscan"
  match:
    command_contains: ["masscan"]
  predict:
    - event: execve
      args: {pathname: "/usr/bin/masscan"}
  ttp: "T1595.001"

# rules/extraction/credential.yaml
- id: "ext-shadow-read"
  match:
    command_contains: ["cat /etc/shadow", "grep.*shadow", "john", "hashcat"]
  predict:
    - event: openat
      args: {pathname: "/etc/shadow"}
  ttp: "T1003"

# rules/extraction/lateral.yaml
- id: "ext-ssh-lateral"
  match:
    command_contains: ["ssh ", "scp "]
  predict:
    - event: execve
      args: {pathname: "/usr/bin/ssh"}
    - event: connect
      args: {remote_port: 22}
  ttp: "T1021.004"

# rules/extraction/exploit.yaml
- id: "ext-python-exploit"
  match:
    command_contains: ["python", "exploit", "payload", "reverse"]
  predict:
    - event: execve
      args: {pathname_prefix: "python"}
    - event: connect
      args: {}    # RCE 通常产生 connect（reverse shell）
  ttp: "T1059.006"
```

---

## Prediction Matcher

### 数据结构

```go
// pkg/matcher/prediction.go
type Prediction struct {
    RunID      string    `json:"run_id"`
    AgentStep  int       `json:"agent_step"`
    Node       string    `json:"node"`
    TimeWindow int       `json:"time_window"` // 秒
    SubmittedAt time.Time `json:"submitted_at"`

    ExpectedEvents []ExpectedEvent `json:"expected_events"`
    TTP            string `json:"ttp,omitempty"`
    ExtractorRule  string `json:"extractor_rule,omitempty"` // 哪条规则产生了这个预测
}

type ExpectedEvent struct {
    Name string         `json:"name"`           // "execve", "connect", "openat"
    Args map[string]any `json:"args,omitempty"`  // partial match
}
```

### 匹配算法

```
对每条 Prediction:
  1. 时间窗口过滤: events where ts ∈ [submitted_at, submitted_at + time_window]
  2. 节点过滤: events where node_id == prediction.node
  3. 对每条 ExpectedEvent:
       找窗口内第一条满足：
         event.name == expected.name
         AND expected.args ⊆ event.args（partial match）
  4. 命中率 = 命中的 ExpectedEvent 数 / 总 ExpectedEvent 数

Partial match 规则:
  - 字符串: 精确匹配 或 glob（*）
  - pathname_prefix: event.args.pathname 以 prefix 开头
  - remote_port: 精确匹配 或 列表成员
  - 空 args {}: 只匹配 event name
  - CIDR (remote_addr): net.Contains(cidr, event.args.remote_addr)
```

### Reward 公式

```
per_step_reward =
    + w_hit  × prediction_hit_rate          // 预测命中（主要正向）
    - w_miss × (1 - prediction_hit_rate)    // 预测未命中（轻微负向）
    - w_unscripted × unscripted_rate        // 做了但没预测（惩罚隐藏行为）
    + w_ttp  × ttp_novelty                  // 新 TTP 类型（探索奖励）

默认权重: w_hit=1.0, w_miss=0.3, w_unscripted=1.5, w_ttp=0.2

episode_reward = mean(per_step_reward) + terminal_bonus
```

`unscripted_rate` 权重最高，防止 agent 在 hook 盲区做事。

---

## IoC Rule Engine（独立于 Prediction）

IoC 引擎独立扫描**全量 tracee 事件**，不依赖 agent 预测。用于：
1. 覆盖 hook 未拦截到的行为（agent 直接改脚本绕过某些工具）
2. 作为 Prediction Matcher 的验证（两者同时命中 = 高置信度）
3. 提供 baseline 分析（对比"已知攻击工具"的覆盖率）

规则格式（`rules/ioc/`）：

```yaml
# rules/ioc/execution.yaml
- id: "ioc-exec-scanner"
  ttp: "T1595.001"
  event: execve
  match:
    args.pathname: ["/usr/bin/nmap", "/usr/bin/masscan", "/usr/bin/zmap"]

- id: "ioc-exec-shell-from-webproc"
  ttp: "T1059.004"
  event: execve
  match:
    args.pathname: ["/bin/sh", "/bin/bash"]
    processName: ["php-fpm", "httpd", "nginx", "node"]  # 父进程是 web server

# rules/ioc/credential.yaml
- id: "ioc-shadow-read"
  ttp: "T1003"
  event: openat
  match:
    args.pathname: ["/etc/shadow", "/root/.ssh/id_rsa", "/root/.ssh/id_ed25519"]
```

---

## Match Report

```go
// pkg/matcher/match_report.go
type MatchReport struct {
    RunID     string    `json:"run_id"`
    GeneratedAt time.Time `json:"generated_at"`

    Steps []StepReport `json:"steps"`

    // Episode 级摘要
    EpisodePredictionHitRate float64 `json:"episode_prediction_hit_rate"`
    TTPsCovered              []string `json:"ttps_covered"`
    EpisodeReward            float64 `json:"episode_reward"`
}

type StepReport struct {
    AgentStep  int    `json:"agent_step"`
    Node       string `json:"node"`
    TTP        string `json:"ttp,omitempty"`
    ExtractorRule string `json:"extractor_rule,omitempty"`

    MatchedEvents   []MatchedEvent `json:"matched_events"`
    UnmatchedPreds  []ExpectedEvent `json:"unmatched_predictions"`
    UnscriptedIoCs  []IoCMatch `json:"unscripted_iocs"`   // IoC 触发但无预测

    PredictionHitRate float64 `json:"prediction_hit_rate"`
    UnscriptedRate    float64 `json:"unscripted_rate"`
    StepReward        float64 `json:"step_reward"`
}

type MatchedEvent struct {
    Event             sensor.Event `json:"event"`
    MatchedPrediction bool         `json:"matched_prediction"`
    IoC               string       `json:"ioc,omitempty"`
}

type IoCMatch struct {
    Event  sensor.Event `json:"event"`
    RuleID string       `json:"rule_id"`
    TTP    string       `json:"ttp"`
}
```

---

## 文件结构增量

```
sysbox/
├── pkg/
│   ├── hook/
│   │   ├── hook.go            # ToolCall 结构 + Hook wrapper 接口
│   │   ├── extractor.go       # Extractor 接口 + RuleExtractor 实现
│   │   ├── writer.go          # PredictionWriter（写 predictions.jsonl）
│   │   └── hook_test.go
│   │
│   └── matcher/
│       ├── prediction.go      # Prediction + ExpectedEvent 数据结构
│       ├── matcher.go         # Prediction Matcher 核心逻辑
│       ├── ioc.go             # IoC Rule Engine（独立扫描）
│       ├── match_report.go    # MatchReport + Reward 计算
│       └── matcher_test.go
│
├── rules/
│   ├── extraction/            # Hook extractor 规则（tool_call → syscalls）
│   │   ├── network_scan.yaml
│   │   ├── credential.yaml
│   │   ├── lateral.yaml
│   │   └── exploit.yaml
│   └── ioc/                   # IoC 独立检测规则
│       ├── execution.yaml
│       ├── credential.yaml
│       ├── network.yaml
│       └── lateral.yaml
│
├── cmd/sysbox/commands/
│   ├── predict_cmd.go         # sysbox predict list/submit
│   └── match_cmd.go           # sysbox match run / report
│
└── tests/e2e/
    └── matcher_test.go        # TestMatcherBasic / TestMatcherUnscripted / TestMatcherWebshell
```

---

## 实现 Tasks

### Task 1: pkg/hook — Hook Layer + Rule Extractor

- `hook.go`: ToolCall 结构，Hook wrapper
- `extractor.go`: RuleExtractor（加载 `rules/extraction/*.yaml`，匹配 command 关键词）
- `writer.go`: PredictionWriter（append to `predictions.jsonl`）
- `rules/extraction/*.yaml`: 4 个规则文件，各 ≥3 条规则
- 单元测试

### Task 2: pkg/matcher — Prediction + IoC + MatchReport

- `prediction.go`: 数据结构 + JSONL IO
- `matcher.go`: 时间窗口过滤 + partial arg match + IoC scan
- `ioc.go`: IoC Rule Engine（加载 `rules/ioc/*.yaml`）
- `match_report.go`: MatchReport 生成 + Reward 计算
- `rules/ioc/*.yaml`: 4 个规则文件
- 单元测试（命中/未命中/unscripted 三种路径）

### Task 3: CLI 命令

```bash
# 查看当前 run 的 predictions
sysbox predict list --state runs/default/state.json

# 手动提交 prediction（调试用）
sysbox predict submit --node node_a --step 5 --command "nmap 10.0.1.0/24"

# 运行 Matcher
sysbox match run \
    --events runs/default/events.jsonl \
    --predictions runs/default/predictions.jsonl \
    --rules rules/ \
    --output runs/default/match_report.json

# 显示摘要
sysbox match report --run runs/default/
```

### Task 4: Event Schema 更新

- 更新 `sensor.Event`（添加 Matcher 填充字段，保持向后兼容）
- 原始 `events.jsonl` 只含 tracee 数据
- Matcher 输出 `annotated_events.jsonl`

### Task 5: Python Hook SDK（供 agent 框架接入）

```python
# python/sysbox_hook/hook.py
# 供 Claude Code / LangChain / AutoGen 等 agent 框架使用

from sysbox_hook import SysboxHook

hook = SysboxHook(
    predictions_file="runs/default/predictions.jsonl",
    rules_dir="rules/extraction/",
    run_id="langfuse-run-abc",
)

# 包装 tool executor
@hook.wrap_tool(node="node_a")
def bash_exec(command: str) -> str:
    # 原始 tool 逻辑
    return subprocess.run(command, shell=True, capture_output=True).stdout
```

### Task 6: E2E 测试

```
TestMatcherBasic（需 root + docker）：
  1. sysbox apply field（node + network）
  2. sensor start（tracee 全量）
  3. hook.Extract("nmap 10.0.1.0/24") → prediction
  4. SSH into node_a: nmap
  5. sysbox match run
  6. Assert: prediction_hit_rate == 1.0, ttp == "T1595.001"

TestMatcherUnscripted（需 root + docker）：
  1. 启动 field + sensor
  2. 不提交任何 prediction
  3. SSH into node_a: nmap
  4. sysbox match run
  5. Assert: unscripted_iocs 包含 nmap execve
  6. Assert: episode_reward < 0

TestMatcherWebshell（需 root + docker + web container）：
  1. 启动 field（含 php-based web 服务）
  2. sensor start
  3. hook.Extract("curl http://node_b/shell.php?cmd=cat+/etc/passwd") → prediction
  4. 发 HTTP exploit
  5. sysbox match run
  6. Assert: openat(/etc/passwd) 被命中，无需 cgroup
```

---

## IoC 提取模型训练路径（Phase 3.1+）

Phase 3.0 用纯规则，足够覆盖常见攻击工具。模型化在 Phase 3.1 进行：

```
训练数据格式：
  input:  {tool_name, command, context}
  label:  {predicted_events}（从 match_report 提取，hit = 正样本）

训练信号来源：
  正样本：tool_call 的 predicted_events 在 match_report 里有命中
  负样本：predicted_events 在 match_report 里未命中

基础模型选型：
  Qwen-2.5-7B-Instruct（指令微调，支持结构化输出）
  目标：< 100ms 推理（tool call 是同步调用，需要快）

输入 token 上限：
  tool_name + command + context（CoT 最近 3 步） ≤ 512 tokens
```

---

## Phase 3 完成检查清单

**Hook Layer：**
- [ ] RuleExtractor 加载 `rules/extraction/*.yaml` 正确
- [ ] `hook.Extract("nmap ...")` 输出 `{execve, connect}` 预测
- [ ] PredictionWriter 写入 predictions.jsonl
- [ ] Python SDK `SysboxHook.wrap_tool()` 可用

**Prediction Matcher：**
- [ ] 时间窗口过滤正确
- [ ] Partial match 支持精确/glob/pathname_prefix/CIDR/列表
- [ ] Reward 公式：命中率/漏报/unscripted 三项权重

**IoC Engine：**
- [ ] `rules/ioc/*.yaml` 加载正确
- [ ] `ioc-exec-shell-from-webproc` 正确检测 webshell（父进程为 web server）

**CLI：**
- [ ] `sysbox match run` 输出 `match_report.json`
- [ ] `sysbox match report` 打印可读摘要

**E2E：**
- [ ] TestMatcherBasic PASS（prediction_hit_rate == 1.0）
- [ ] TestMatcherUnscripted PASS（reward < 0）
- [ ] TestMatcherWebshell PASS（webshell 无需 cgroup）

---

## Phase 4 起步提示

- **IoC 提取模型**（Phase 3.1）：用 match_report 数据 fine-tune Qwen-7B，自监督循环
- **Firecracker substrate**：microVM 替代 Docker，sensor 通过 virtio-serial 回传事件
- **Replay Bundle**：`sysbox bundle create` 打包 events + match_report + field.hcl
- **Multi-agent**：多 agent 并发演练，按 run_id 隔离归因
- **Continuous mode**：长时间 field，Matcher 实时流式处理

---

*预估工作量：6 Tasks × 1-2 天 ≈ 8-10 人天*
