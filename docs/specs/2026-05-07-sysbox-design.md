# sysbox 设计规格

> **日期**: 2026-05-07
> **状态**: Brainstorming 完成，待 implementation plan
> **作者**: jiandong + Claude
> **前置阅读**: `sysbox/references/docs/` 下的 sysfield / DeepSeek DSec / SysArmor / ACP 材料

---

## 0. 一句话定位

> **sysbox 是 AI 红队的 Terraform —— 一键搭起 Linux 攻防战场，让 AI 进去打；数据自动带标注、AI 越打越强、攻守双方共同进化。**

---

**从问题到方案（Feynman 式技术路线）：**

**问题**：训练一个能识别真实 APT 的检测模型，你需要大量带标签的"这是攻击 / 这不是攻击"系统调用数据。这种数据极难拿到——DARPA 数据集几年前就停更，雇人工红队代价高、规模小。

**自然的想法**：让 AI 当红队。LLM 越来越会玩 Linux，给它一台靶机和一个目标，它自己摸索。规模瓶颈解除。

**新问题**：AI 打完，你怎么知道哪些事件是它干的？不知道就没法打标签。

sysbox 分三步解决。

**第一步，用内核把攻击范围圈住。** Linux 的 cgroup 不只是资源限制工具，它也是一张"进程会员表"——内核强制让所有 fork 子进程继承父的 cgroup。sysbox 给 agent 单开一个 cgroup，它衍生多少层进程、注入多少次，都自动进表。"哪些进程是 agent 干的"于是成为**内核事实**，不靠猜。

**第二步，让 agent 自报意图，sensor 做核验。** agent 行动前的 chain-of-thought 早就写出来了："我要做 persistence，会写 /etc/crontab，一分钟后 crond 会跑 nc 回连 C2"。把这种预测结构化成 `(type, key fields, time window)` 的 Prediction Schema，然后在 sensor 事件里机械地找匹配——匹配上的事件直接继承 agent 的 intent/TTP 标签。**sysbox 不做判断，只做对齐**。标签权威来自 "agent 声明 × sensor 事实核验"，没有启发式、没有规则魔法。

**第三步，把对账结果反喂 agent 做 RL。** agent 预测 3 件事、实际发生 2 件——"预测准确率"直接成为稠密 reward。这解决了 agent 安全 RL 的最大瓶颈：稀疏 reward（几十步后才知道成败）。**agent 预测越准 → 标签越细 → detector 训练越好 → detector 反过来又能审核 agent 声明真伪**。红蓝在同一份数据上对偶演化——这是 sysbox 超越"一次性数据生成工具"的地方。

**工程上，sysbox 像 Terraform**：一份 HCL 描述拓扑（容器 + microVM + VM 混搭）、sensor 配置、SSH 入口；`apply` 起场景、`destroy` 拆。观测用 eBPF（Tracee 默认，可换 SysArmor）。Agent 本身不是 sysbox 的一部分——任何能说 SSH/HTTP 的 agent 都能接（Claude Code、LangGraph、自研 policy），入场前调一下 `sysbox session register` 完事。

---

## 1. 项目缘起与边界

### 1.1 现有工具的 gap

- **sysfield**：Playbook-driven，攻击步骤预设、Agent 按部就班执行，无法承载自主 agentic 渗透
- **DeepSeek DSec**：给 RL rollout 提供统一沙盒抽象，但不对外开源且不做安全标注
- **containerlab**：能搭多节点拓扑，但不管 microVM/VM、不管观测、不管 session 归属
- **DARPA TC / OpTC**：权威 GT，但人工 red team 成本极高，不可规模化

### 1.2 sysbox 的 niche

把上面四者的优点组合：
- 像 Terraform 一样**声明式**起拓扑
- 像 DSec 一样**统一抽象** container/microVM/VM 三种基底
- 像 SysArmor 一样**深度观测**（eBPF 全栈）
- 像 DARPA TC 一样**产出带 GT 的科研 dataset**
- 但把"攻击执行"这一环**交给外部 agent**（LLM-driven），这样能无限规模化产生数据

### 1.3 明确不做的事

为避免 scope creep，以下明确**不在** sysbox 职责内：

| 不做 | 谁做 |
|---|---|
| Agent 的 LLM 调用、prompt、推理 | Claude Code / LangGraph / 研究者自己 |
| Agent 的 trajectory 记录 UI | Langfuse / LangSmith / Phoenix / OTEL |
| Agent 的 MCP server / ACP wiring | 研究者在 experiment 层自写 |
| 防御系统（IDS / EDR） | SysArmor / 其他被评测的系统 |
| 多机集群调度（MVP 阶段） | 单机起步，v2 再做 sysbox-runtimed |
| K8s runtime | 核心永不上 K8s，只可能包 operator wrapper |
| Windows guest 观测（MVP） | v2 做，用 ETW/Sysmon |

**sysbox 只做三件事**：
1. **拓扑**：HCL + providers（docker / firecracker / libvirt / network / sensor）
2. **观测**：节点级全栈 eBPF 采集 + cgroup v2 session 锚定
3. **对齐**：Agent predictions × Sensor events 机械匹配，产出 labeled dataset + Match Report

---

## 2. 用户和核心场景

### 2.1 目标用户

- **检测算法团队**：需要大规模带标注的 syscall/审计事件 dataset 来训练/评估 EDR、UEBA、溯源模型
- **安全 AI 研究**：需要 agentic 红队的 trajectory + 对应环境事件，用于 RL、evaluation、benchmark
- **学术**：发论文、做 benchmark、需要可复现的 artifact

### 2.2 典型使用流程

```bash
# 1. 写一份 HCL 描述"场景 / 舞台"
vim scenarios/log4j-corp-lan.sysbox.hcl

# 2. 把拓扑+sensor 起来
sysbox init
sysbox plan
sysbox apply                        # field 就绪

# 3. 由研究员的 experiment 代码驱动 agent 攻击 field
#    (sysbox 不参与 agent 的推理；只提供 SDK 帮 agent 声明预期效果)
python experiments/claude_pentest.py
#   ├─ sysbox.session(...) context manager 开 session, 建 cgroup 锚
#   ├─ 每步动作前: session.declare_step(intent=..., predicted_effects=[...])
#   │     (或跳过声明, 由步骤 4 的 CoT 抽取器事后补齐)
#   ├─ 用 SSH/HTTP 接入 attacker 节点执行动作
#   ├─ 通过 Langfuse/OTEL 记 agent trajectory (含预测)
#   └─ agent 跑到时间/步数上限或完成 objective

# 4. 事后标注 (对齐 predictions × events)
sysbox dataset label runs/<run_id> \
    --trajectory "langfuse://org/proj/sessions/<id>" \
    [--extract-predictions-from-cot]     # 可选: 从 CoT 自动抽 predictions
# 产出:
#   runs/<run_id>/labeled/
#     ├─ labeled-events.jsonl.zst       # 带 intent/TTP 的事件流 (给 detector 训练)
#     ├─ match-report.json              # 每步 prediction 匹配率 (给 agent RL reward)
#     ├─ provenance-graph-<session>.json
#     └─ manifest.json                  # dataset 元数据 + realism scorecard

# 5. 发布 / 分享
sysbox replay bundle runs/<run_id>      # 输出自包含 .tar.zst, 别人可字节级复现 label

sysbox destroy
```

---

## 3. 系统总览

### 3.1 组件全景图

```
┌─────────────────────────────────────────────────────────────────┐
│  sysbox CLI (Go 单二进制)                                         │
│                                                                 │
│   field.sysbox.hcl ──► HCL parser ──► resource graph ──► plan   │
│                                                 │               │
│                                                 ▼               │
│   State file (runs/<run_id>/state.json)                         │
│                                                                 │
│   Commands:                                                     │
│     init / plan / apply / destroy / state / show / output       │
│     dataset label / dataset stats                               │
│     session register / session start                            │
│     replay bundle / replay extract                              │
│     labeler validate (benign-baseline / scenario / darpa-tc)    │
└─────────────────────────────────────────────────────────────────┘
              │ go-plugin gRPC
              ▼
  ┌──────────────────────┐  ┌──────────────────────┐
  │ Substrate Providers  │  │ Infra Providers      │
  │                      │  │                      │
  │ docker               │  │ network (bridge/veth │
  │ firecracker          │  │         nftables)    │
  │ libvirt              │  │ sensor (tracee MVP)  │
  └──────────────────────┘  └──────────────────────┘
              │                       │
              ▼                       ▼
  ┌──────────────────────────────────────────────────────┐
  │  Field (Linux kernel objects, no sysbox daemon)      │
  │                                                      │
  │   节点群 (container / microVM / VM)                  │
  │   网络 (netns / bridge / veth / tap)                 │
  │   Sensor Collector daemon (一个 run 一个)            │
  └──────────────────────────────────────────────────────┘
        ▲                                     ▲
        │ SSH / HTTP / TCP                    │ events
        │                                     │
  ┌──────────────────┐           ┌──────────────────────────┐
  │ Agent Experiment │           │ sysbox Labeler            │
  │  (研究员的代码)   │──────────►│                          │
  │                  │ trajectory│  Layer A: cgroup 锚定     │
  │  • LLM           │  (含      │  Layer B: Prediction ×    │
  │  • Langfuse/OTEL │  predicted│          Event Matcher    │
  │    + predictions │  effects) │  Layer C: Match Report    │
  │  • SSH/HTTP/...  │           │                          │
  └──────────────────┘           └───────────┬──────────────┘
                                             ▼
                                   labeled-events.jsonl.zst
                                   provenance-graph.json
                                   match-report.json  ← RL reward 原料
                                   manifest.json
                                   replay-bundle.tar.zst
```

### 3.2 关键设计原则

1. **Terraform 心智为第一等约束**：HCL + state file + plan/apply + providers。不引入 always-on daemon。
2. **所有东西都是 resource**（除了 agent session）：节点/网络/firewall/router/sensor/SSH access 全是 HCL 资源；agent 执行是**事件性行为**，不是资源。
3. **三个数据面分层**：
   - Trajectory（外部 AI 可观测性栈负责）
   - Events（sysbox sensor 负责）
   - Ground Truth（sysbox labeler 对齐两者产出）
4. **provider 插件生态**：go-plugin gRPC；docker/firecracker/libvirt 是 3 个平行 provider，不是 core 写死。
5. **跨 substrate 统一抽象**：用户 HCL 写 `substrate = substrate.firecracker.xxx` 切换底层，资源 schema 保持一致。
6. **标注责任转移到 agent + sensor**：sysbox 不做"判断什么是攻击"的启发式；labels 来自 agent 显式声明的预测与 sensor 事件的机械匹配。规则/启发式只作为 matching 失败时的兜底。
7. **数据为双目标服务**：产出的 dataset 同时可供 (a) 检测算法训练、(b) agent RL reward。这两者在同一份数据上对偶演化，是 sysbox 的核心 research story。

---

## 4. HCL Schema

### 4.1 资源类型清单

| 资源 | 作用 | 示例字段 |
|---|---|---|
| `substrate` block | 声明 substrate 实例 | `type`, `alias`, `kernel_image` (fc), `uri` (libvirt) |
| `sysbox_image` | 镜像声明 | `substrate`, `rootfs`/`docker_ref`, `size` |
| `sysbox_node` | 拓扑节点 | `image`, `substrate`, `links`, `vcpus`, `memory` |
| `sysbox_network` | L2/L3 网络 | `cidr`, `type` (bridge/ovs) |
| `sysbox_firewall` | nftables 规则集 | `attach_to`, `rules` |
| `sysbox_router` | NAT/转发节点 | `interfaces`, `nat_from`, `nat_to` |
| `sysbox_ssh_access` | SSH 访问糖 | `node`, `authorized_keys`, `bind_ip`, `port` |
| `sysbox_sensor` | 观测声明 | `targets`, `implementation`, `profile`, `sink` |

### 4.2 完整示例

```hcl
# field.sysbox.hcl — 三段拓扑示例

# --- Substrates ---
substrate "docker" { alias = "light" }

substrate "firecracker" {
  alias        = "microvm"
  kernel_image = "./assets/vmlinux-6.8"
}

substrate "libvirt" {
  alias = "fullvm"
  uri   = "qemu:///system"
}

# --- Images ---
resource "sysbox_image" "kali" {
  substrate = substrate.firecracker.microvm
  rootfs    = "./assets/kali-rolling.ext4"
  size      = "4GiB"
}

resource "sysbox_image" "dvwa" {
  substrate  = substrate.docker.light
  docker_ref = "vulhub/log4j-rce:2.14.1"
}

resource "sysbox_image" "mysql" {
  substrate  = substrate.docker.light
  docker_ref = "mysql:8.0"
}

# --- Networks ---
resource "sysbox_network" "dmz"          { cidr = "10.0.1.0/24" }
resource "sysbox_network" "internal"     { cidr = "10.0.2.0/24" }
resource "sysbox_network" "attacker_net" { cidr = "192.168.100.0/24" }

# --- Firewalls ---
resource "sysbox_firewall" "dmz_ingress" {
  attach_to = sysbox_network.dmz.id
  rules = [
    { proto = "tcp", dport = 80,  action = "accept" },
    { proto = "tcp", dport = 443, action = "accept" },
    { proto = "all", action = "drop" },
  ]
}

# --- Router (DMZ ↔ Attacker 互通) ---
resource "sysbox_router" "edge" {
  substrate = substrate.docker.light
  image     = "sysbox/linux-router:1.0"
  interfaces = {
    wan = { network = sysbox_network.attacker_net.id, ip = "192.168.100.1/24" }
    lan = { network = sysbox_network.dmz.id,          ip = "10.0.1.1/24"      }
  }
  nat_from = sysbox_network.attacker_net.id
  nat_to   = sysbox_network.dmz.id
}

# --- Nodes ---
resource "sysbox_node" "attacker" {
  name      = "attacker"
  image     = sysbox_image.kali.id
  substrate = substrate.firecracker.microvm
  vcpus     = 2
  memory    = "2GiB"
  links = [
    { network = sysbox_network.attacker_net.id, ip = "192.168.100.10/24", gw = "192.168.100.1" },
  ]
}

resource "sysbox_node" "web" {
  name      = "web"
  image     = sysbox_image.dvwa.id
  substrate = substrate.docker.light
  links = [
    { network = sysbox_network.dmz.id,      ip = "10.0.1.10/24", gw = "10.0.1.1" },
    { network = sysbox_network.internal.id, ip = "10.0.2.10/24" },
  ]
}

resource "sysbox_node" "db" {
  name      = "db"
  image     = sysbox_image.mysql.id
  substrate = substrate.docker.light
  env = { MYSQL_ROOT_PASSWORD = "weak-password-123" }
  links = [{ network = sysbox_network.internal.id, ip = "10.0.2.20/24" }]
}

# --- SSH access (糖) ---
resource "sysbox_ssh_access" "attacker" {
  node            = sysbox_node.attacker.id
  authorized_keys = [file("./pubkey-experiment.pub")]
  bind_ip         = "192.168.100.10"
  port            = 22
}

# --- Sensor ---
resource "sysbox_sensor" "all" {
  targets = [
    sysbox_node.attacker.id,
    sysbox_node.web.id,
    sysbox_node.db.id,
  ]
  implementation = "tracee"
  profile        = "standard"
  sink           = "file://./runs/${var.run_id}/events"
}
```

---

## 5. Substrate 统一抽象

### 5.1 Substrate Go 接口

```go
type Substrate interface {
    Name() string
    Capabilities() Capabilities
    
    PrepareImage(ctx, ImageSpec) (ImageRef, error)
    CreateNode(ctx, NodeSpec) (NodeHandle, error)
    StartNode(ctx, NodeHandle) error
    StopNode(ctx, NodeHandle) error
    DestroyNode(ctx, NodeHandle) error
    
    ExecInNode(ctx, NodeHandle, ExecSpec) (ExecResult, error)
    CopyToNode(ctx, NodeHandle, src, dst string) error
    CopyFromNode(ctx, NodeHandle, src, dst string) error
    
    AttachNIC(ctx, NodeHandle, NIC) error
    
    ObservationHook(ctx, NodeHandle) (ObservationTarget, error)
}

type Capabilities struct {
    SharedKernel    bool    // docker=true, fc=false, libvirt=false
    SupportsWindows bool    // libvirt=true, others=false
    BootTime        string  // "ms" | "seconds"
    NICType         string  // "veth" | "tap"
}

type ObservationTarget struct {
    Kind   string  // "host-pid-namespace" | "virtio-serial"
    Value  string  // PID / socket path
}
```

### 5.2 NIC 抽象

`NIC` 是 network provider 创建好、准备塞进节点的网卡：

```go
type NIC struct {
    Kind     string   // "veth" | "tap"
    HostEnd  string   // 宿主机一侧的设备名
    GuestEnd string   // veth 时是节点内设备名；tap 时为空
    MAC      string
    IP       string
    Gateway  string
    MTU      int
}
```

Network provider 负责创建 veth 对/TAP；Substrate 负责把它挂到节点。

### 5.3 ObservationHook 的跨 substrate 实现

| Substrate | ObservationTarget |
|---|---|
| Docker | `{Kind: "host-pid-namespace", Value: <container root PID>}` |
| Firecracker | `{Kind: "virtio-serial", Value: "/run/sysbox/obs-<vm-id>.sock"}` |
| libvirt | `{Kind: "virtio-serial", Value: "/run/sysbox/obs-<domain-uuid>.sock"}` |

Sensor provider 根据 `Kind` 分发到对应采集实现（host eBPF / guest-sensor over virtio-serial）。

### 5.4 Apply 图执行顺序

```
1. substrate 实例化 (Docker socket/FC binary/libvirt 可用性检查)
2. sysbox_image (pull/build rootfs)
3. sysbox_network (netns + bridge + IP)
4. sysbox_firewall (nftables 规则加载)
5. sysbox_node (分配节点但不连网)
6. sysbox_link (veth/TAP 创建 + AttachNIC + IP 配置)
7. sysbox_router (特殊 node + ip_forward + MASQUERADE)
8. sysbox_ssh_access (配 authorized_keys + sshd 预配置 wrapper)
9. sysbox_sensor (host sensor + guest-sensor 注入 + collector 启动)
```

`destroy` 反向拓扑序。

---

## 6. Session 归属与追踪（cgroup v2 + 预测匹配）

### 6.1 核心原理

- **锚定** 来自 cgroup v2 成员身份（内核强制继承，兜底永远可靠）
- **细粒度 intent** 来自 agent predictions × sensor events 的 matcher

```
Layer 1 (kernel):   cgroup v2     → attack 进程确定归属 (是否)
Layer 2 (labeler):  prediction 匹配 → 细粒度 intent/TTP 标签 (为什么)
```

**Session 的 cgroup 成员身份是永不丢的底层真实**；prediction matching 为每个事件追加更丰富的 intent/TTP/step 维度标签。匹配失败的 cgroup 事件仍然 `is_attack=true`，只是 `intent=null`。

### 6.2 Session 启动流程

```
实验层调用:
  sysbox session register \
    --node attacker \
    --source 192.168.100.1 \
    --session-id <id> \
    --mitre-ttp T1059.004 \
    --expires-in 60s

然后:
  agent 通过 SSH 进入 attacker:22
  sshd 的 ForceCommand wrapper 被触发:
    → 查询 collector: "来自 192.168.100.1 的 session 是哪个?"
    → 得到 session-id
    → cgcreate /sysbox.slice/session-<id>.scope/
    → cgexec -g ... bash
  入口 bash 进入 cgroup
  所有 fork/exec 子孙自动继承
```

### 6.3 为什么 cgroup 不影响攻击 realism

- cgroup 创建时**不启用任何 controller**（cpu/memory/pids 都不开）
- 纯 membership tracker，attacker 行为完全不受限
- `cgroup_id` 字段在导出 dataset 前被 scrub，算法训练看不到

### 6.4 跨节点 session 传播

由 **collector 实时处理**，不靠事后猜：

```
attacker-session 内进程 connect() 10.0.1.10:80
  → collector 捕获事件，登记到 CrossNodePropagation
  → collector 推送指令到 web 节点的 guest-sensor:
      "期望 30s 内有入口从 10.0.1.10 进来, 归属 session-<id>"

web 节点 nginx accept() from 10.0.1.10
  → guest-sensor 检测到新入口
  → 查 collector 期望表，命中
  → 在 web 节点内部 cgcreate + cgexec 把衍生进程塞进 session cgroup
  → 后续事件 cgroup_id 自动对应 session-<id>
```

---

## 7. Ground Truth 标注器

### 7.1 设计哲学：从 rule-based 到 prediction-matching

传统 APT 数据集标注（DARPA TC / OpTC / ATLAS）本质是**标注者用启发式判断"哪些事件是攻击后果"**。这条路有两个硬伤：
- 启发式规则难以覆盖所有 TTP，维护成本高
- 标注权威来自"人的判断"，论文审稿和复现争议多

sysbox 采用**翻转的视角**——利用 agentic 场景的独特结构：

```
                  优点                       痛点
─────────────────────────────────────────────────────────
Agent RL:        有「意图」(CoT)             缺「客观结果」
Syscall 标注:    有「客观结果」(kernel truth) 缺「意图」
```

**两者互为表里**。Sysbox 不做判断，只做**对齐**：让 agent 声明每步的预期效果（predicted effects），用 sensor 事件做机械核验。匹配上的事件，intent 从 agent 声明直接继承；匹配失败的事件，cgroup 兜底标 attack。

**这样 labels 的权威来自**：
- Agent 的 CoT（意图是主观声明，但 CoT 可审计）
- Sensor 的 syscall（事实是内核 ground truth）
- sysbox 只做 timestamp + cgroup + pattern 的机械 join

**没有启发式判断，没有 sysbox 团队的偏见注入。** 规则仅用于 matching 失败时的兜底，不是主路径。

### 7.2 三层架构总览

```
┌─────────────────────────────────────────────────────────────┐
│  Layer A: Cgroup 锚定（事实）                                │
│    内核强制 session cgroup 成员身份                          │
│    → 保底的 is_attack=true（永不错标）                       │
└─────────────────────────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer B: Prediction × Event Matcher（对齐）                 │
│    agent 每 step 声明 predicted_effects                      │
│    labeler 机械地在 event 流里找匹配                         │
│    → 匹配上 → 给 event 打 step 的 intent/TTP 标签            │
│    → 未匹配 predictions → 报告 "step 失败 / 预测错"          │
│    → 未预测 events（但在 cgroup 里）→ 报告 "agent 未声明动作"│
└─────────────────────────────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer C: Match Report（RL reward 原料）                     │
│    每步的 prediction hit/miss 比例                           │
│    整体 dataset 的 coverage / precision / unexpected 指标    │
│    → 直接喂给 agent RL training 做 per-step reward shaping   │
└─────────────────────────────────────────────────────────────┘
```

### 7.3 Prediction Schema

Agent 每执行一步动作，在 trajectory 里声明"预期后果"。Schema 用"**type + required 字段 + optional 字段 + time_window**"结构。

**基础 effect type（MVP 支持）：**

| Type | Required 字段 | Optional 字段 |
|---|---|---|
| `process_spawn` | `comm_pattern` 或 `argv_contains` | `ppid_comm`, `uid`, `cwd_pattern` |
| `file_write` | `path_pattern` | `content_contains`, `mode` |
| `file_read` | `path_pattern` | `size_range` |
| `file_delete` | `path_pattern` | — |
| `permission_change` | `path_pattern`, `new_mode` 或 `new_owner` | — |
| `network_connect` | `dst_cidr` 或 `dst_ip` 或 `dst_port` | `proto`, `count_range` |
| `network_listen` | `bind_port` | `proto` |
| `process_kill` | `target_comm` | `signal` |
| `module_load` | `module_name_pattern` | — |
| `ptrace` | `target_comm` 或 `target_pid_tag` | — |
| `shm_create` | `name_pattern` | — |
| `future_process` | `comm_pattern` + `ppid_comm` | `argv_contains`, `expected_within` |

`future_process` 是核心 TTP 的关键——它覆盖所有"打破进程血缘的持久化"（crontab / systemd / rc.local 等）。

**完整示例：**

```yaml
# 一个 "crontab 持久化 + 反弹 shell" 步骤的预测

step_id: step-007
intent: "persistence via crontab + reverse shell setup"
mitre_ttp: T1053.003
kill_chain_phase: persistence

predicted_effects:
  
  # 写 crontab 是立即效果
  - type: file_write
    required:
      path_pattern: "/etc/crontab"
      content_contains: "/tmp/.nc"
    time_window: immediate
  
  # crond 60-90s 后会触发执行
  - type: future_process
    required:
      comm_pattern: "nc"
      ppid_comm: "crond"
    optional:
      argv_contains: ["192.168.100.1", "4444"]
    time_window: "60s-90s after step"
  
  # nc 会连回 C2
  - type: network_connect
    required:
      dst_ip: "192.168.100.1"
      dst_port: 4444
    time_window: "within 120s after step"
```

**Agent 如何产出这些预测？** 三种路径，从最依赖 agent 合作到最不依赖：

| 路径 | 做法 | 侵入性 |
|---|---|---|
| A. 直接 API | 实验层在 agent 工具包装里显式 `session.declare_step(...)` | 需要改 agent harness |
| B. CoT 自动抽取 | 给 agent CoT 跑一个小 LLM classifier 抽 structured predictions | 零侵入，post-hoc |
| C. 混合 | Agent 自己能 declare 就用 A；没 declare 的步骤走 B 兜底 | 渐进式 |

MVP 提供路径 B 的**自动抽取器**（~50 行 Python，用任何 LLM 作为 extractor），这样即使 agent 不改代码也能用。

### 7.4 Matcher 算法

标注过程就是 **两个有序流的 JOIN**：

```
INPUT:
  events:      sensor 产出的 ECS JSON stream (按 ts 排序)
  predictions: agent trajectory 里的 predicted_effects
               (每条带 step_id + time_window + required/optional fields)

ALGORITHM:
  for each prediction P:
    candidates = events where:
      - event.type matches P.type
      - event.ts in P.time_window
      - event.cgroup_id in session (if session-constrained)
    
    for each candidate C in candidates:
      required_score = fraction of P.required that C matches
      optional_score = fraction of P.optional that C matches
      
      if required_score == 1.0:
        if optional_score >= 0.5: STRICT_MATCH
        else:                     LOOSE_MATCH
        C.labels += P.step_id, P.intent, P.mitre_ttp
      else:
        skip (not a match)
    
  # 三类汇报
  matched_predictions    = predictions with >=1 matched event
  unmatched_predictions  = predictions with 0 matched events  (agent "说了没做到")
  unpredicted_events     = cgroup events with no matching prediction (agent "做了没声明")
```

**产出：**
- 事件 → labels（是主要 dataset）
- match report（是 RL reward 原料）

**匹配是纯函数**：同样 events + predictions → 字节级一致的 labels 和 report。

### 7.5 APT TTP 覆盖表

按 MITRE ATT&CK 14 个 Tactic，代表性 TTP 与 sysbox 机制的适配。预测层级分：

| 层级 | 含义 | 处理方式 |
|---|---|---|
| **L1** | 可精确预测（process/file/net 明确签名） | Prediction 严格匹配 |
| **L2** | 关键字段可预测（具体值不可控，但 type+target 可以） | Prediction loose match |
| **L3** | 起点可预测，后续不可控 | 起点匹配 + cgroup 兜底 |
| **L4** | 事件本身 sensor 难见（内存级/硬件级） | Cgroup 兜底 + 记 limitation |

**完整覆盖表：**

| Tactic | Technique | 行为 | 预测层 | sysbox 覆盖 |
|---|---|---|---|---|
| **Initial Access** | T1190 Exploit Public App | 对 Web 发 RCE payload | L1 | ✅ `network_connect` + 服务端 cgroup 传播 |
| | T1133 External Remote Svc | SSH 登入 | L1 | ✅ `network_connect` + session register |
| | T1078 Valid Accounts | 用窃取密码登录 | L1 | ✅ 同上 |
| **Execution** | T1059.004/.006 Shell/Python | 跑 bash/python | L1 | ✅ `process_spawn` 精确 |
| | T1053.003 Cron Jobs | 写 crontab 持久化 | L1+L2 | ✅ `file_write` + `future_process` |
| | T1569 System Services | systemctl 启服务 | L1 | ✅ `file_write` + `future_process` |
| | T1203 Exploit for Exec | 内存损坏漏洞 | **L4** | ⚠️ cgroup 兜底（起始进程在 cgroup） |
| **Persistence** | T1547 Autostart | rc.local/.bashrc | L1 | ✅ `file_write` 精确 |
| | T1505.003 Web Shell | 写 webshell | L1 | ✅ `file_write` + 未来 HTTP 触发 → 跨节点传播 |
| | T1543 Create/Modify Svc | systemd unit | L1 | ✅ `file_write` + `future_process` |
| | T1136 Create Account | 改 /etc/passwd | L1 | ✅ `file_write` |
| | T1574 Hijack Execution | LD_PRELOAD | L1 | ✅ `file_write` /etc/ld.so.preload |
| **Privilege Escalation** | T1548.003 sudo | 执行 sudo | L1 | ✅ `process_spawn` |
| | T1068 Kernel Exploit | Kernel exploit | **L3** | ⚠️ 起点 `process_spawn` 匹配；后续走 cgroup + `permission_change` |
| | T1055 Process Injection | ptrace/process_vm_writev | L2+L3 | ✅ `ptrace` 事件匹配；target 后续走兜底 |
| **Defense Evasion** | T1070.004 File Deletion | rm /var/log/* | L1 | ✅ `file_delete` |
| | T1562.001 Disable Tools | 关 auditd | L1 | ✅ `process_kill` + systemctl op |
| | T1027 Obfuscated Files | 写编码文件 | L1 | ✅ `file_write` |
| | T1014 Rootkit (LKM) | 加载内核模块 | L2+**L4** | ✅ `module_load` 匹配；加载后 kernel 行为 L4 不覆盖 |
| | T1620 Reflective Loading | 纯内存执行 | **L4** | ⚠️ cgroup 兜底，记 limitation |
| | T1218 LOLBins | 滥用合法二进制 | L1 | ✅ `process_spawn` |
| **Credential Access** | T1003.008 /etc/shadow | dump shadow | L1 | ✅ `file_read` 精确 |
| | T1110 Brute Force | 大量登录尝试 | L1 | ✅ `network_connect` + count |
| | T1552.004 SSH Keys | 读 .ssh/id_rsa | L1 | ✅ `file_read` |
| **Discovery** | T1046 Network Scan | nmap | L1+L2 | ✅ `network_connect` + dst_cidr + count |
| | T1082 System Info | uname/os-release | L1 | ✅ `file_read` + `process_spawn` |
| | T1083 File Discovery | find/ls | L1 | ✅ `process_spawn` |
| | T1018 Remote System | ping/arp | L1 | ✅ `network_connect` (ICMP)/arp |
| **Lateral Movement** | T1021.004 SSH | ssh 到内网 | L1 | ✅ `network_connect` + 目标节点 cgroup 实时建立 |
| | T1570 Tool Transfer | scp 恶意 binary | L1 | ✅ `network_connect` + 目标 `file_write` |
| | T1021.001 RDP | Windows 横移 | L1 | ⚠️ 网络层可观测；Windows guest 观测 v2 做 |
| **Collection** | T1005 Local Data | 读敏感文件 | L1 | ✅ `file_read` |
| | T1560 Archive Data | tar/zip | L1 | ✅ `process_spawn` + `file_write` |
| | T1074 Data Staging | 写 staging dir | L1 | ✅ `file_write` |
| **C2** | T1071.001 HTTP C2 | 周期 HTTP callback | L1+L2 | ✅ `network_connect` + recurring pattern |
| | T1573.002 TLS C2 | 加密 C2 | L2 | ✅ flow 结构可预测，content 不可（正常） |
| | T1071.004 DNS C2 | DNS 隧道 | L1 | ✅ DNS query 数量异常 |
| | T1572 Tunneling | SSH 端口转发 | L2 | ✅ `network_connect` + 后续 flow 传播 |
| **Exfiltration** | T1041 Over C2 | 大量 send 到 C2 | L2 | ✅ `network_connect` + 字节量 |
| | T1048.003 DNS Exfil | DNS 外传 | L1 | ✅ DNS query 异常 |
| | T1567.002 Cloud Storage | 上传云 | L1 | ✅ `network_connect` 到云 API + 文件 size |
| **Impact** | T1486 Ransomware | 批量加密 | L1+L2 | ✅ 批量 `file_write` + pattern |
| | T1485 Destruction | 批量 rm/shred | L1 | ✅ `file_delete` |
| | T1496 Resource Hijacking | crypto miner | L1 | ✅ `process_spawn` + network to pool |
| | T1499 Endpoint DoS | fork bomb / mem fill | L1 | ✅ `process_spawn` 爆量 |

**覆盖统计：**

| 覆盖度 | TTP 数 | 占比 | 机制 |
|---|---|---|---|
| L1 完全预测覆盖 | ~28 | 70% | Prediction 严格匹配 |
| L2 关键字段覆盖 | ~8 | 20% | Prediction loose match |
| L3 部分覆盖 | ~2 | 5% | 起点匹配 + cgroup 兜底 |
| L4 依赖兜底 | ~2 | 5% | Cgroup 兜底，记 limitation |

**~95% MITRE TTP 被 prediction + cgroup 兜底覆盖。** 剩余 5% 是所有 eBPF 方案的共同天花板。

### 7.6 Dataset 产出格式

**Layer 1 - Per-event labeled JSONL**

```jsonl
{
  "@timestamp": "2026-05-07T10:29:00.000Z",
  "event": { "category": ["process"], "type": ["start"] },
  "process": { "pid": 9841, "comm": "nc", "ppid": 875 },
  "process.parent": { "pid": 875, "comm": "crond" },
  "sysbox.label": {
    "is_attack": true,
    "attack_source": "session-abc",
    "match_type": "prediction_matched",
    "matched_step": "step-007",
    "matched_prediction_type": "future_process",
    "match_strength": "loose",
    "intent": "persistence via crontab",
    "mitre_ttp": ["T1053.003"],
    "kill_chain_phase": "persistence",
    "trajectory_span_id": "lf-span-abc-7"
  }
}
```

其他 `match_type` 取值：
- `prediction_matched`：匹配上 agent 声明 → label 继承 step intent
- `cgroup_fallback`：在 cgroup 但无 prediction → `is_attack=true`，`intent=null`
- `unmatched_prediction`：agent 声明了但事件未发生 → 不进 dataset，进 match_report
- `benign`：不在 cgroup + 无匹配

**Layer 2 - Provenance graph** 和之前相同，附带每个 node 的 `matched_step` 字段。

**Layer 3 - Match Report**（新增）

```json
{
  "run_id": "run-001",
  "session_id": "session-abc",
  
  "per_step": [
    {
      "step_id": "step-007",
      "intent": "persistence via crontab",
      "predictions_total": 3,
      "predictions_matched_strict": 1,
      "predictions_matched_loose": 1,
      "predictions_unmatched": 1,
      "events_matched": 12,
      "step_verified": true,          // 所有 required predictions matched
      "rl_reward_raw": 0.67           // matched/total
    },
    ...
  ],
  
  "summary": {
    "total_events":                142301,
    "events_prediction_matched":    1038 (85% of attack events),
    "events_cgroup_fallback":        209 (15% of attack events),
    "events_benign":              141054,
    "predictions_total":             47,
    "predictions_matched":           42,
    "predictions_unmatched":          5 (agent 预测了未发生),
    "cgroup_events_without_prediction": 209 (agent 做了未声明)
  }
}
```

**Match Report 的两个用途：**
- **RL reward 信号**：`per_step.step_verified` 和 `rl_reward_raw` 直接进训练循环
- **Dataset 质量诊断**：`unmatched_predictions` 偏高说明 agent 预测能力不足或环境异常；`cgroup_events_without_prediction` 偏高说明 agent 缺少自觉

**Layer 4 - Dataset Manifest** 和之前相同。

### 7.7 验证方法

```bash
sysbox labeler validate --benign-baseline <run>
  # 无 agent 跑拓扑；期望 attack_events=0
  
sysbox labeler validate --scenario <name>
  # 内置已知 GT 的 scenario（10-20 个）；算 P/R/F1
  
sysbox labeler validate --prediction-quality <run>
  # 检查 agent predictions 的覆盖率/准确率
  # 输出：predictions_matched / predictions_total, unpredicted_ratio
  
sysbox labeler validate --against-darpa-tc <path>
  # 用 DARPA TC 数据做交叉验证
```

**Benchmark scenario 的设计**：每个 scenario 附带**手工写好的 predictions**（模拟理想 agent 的完美自我声明），跑完之后对比自动 labeler 输出和手工预期，得到 ground-truth precision/recall。

### 7.8 Known Limitations（诚实列出）

| Limitation | 影响 | 应对 |
|---|---|---|
| Agent 必须能产出结构化 prediction（或用小 LLM 抽） | GPT-4+/Claude Opus 级模型 OK；小模型可能乏力 | 路径 B 自动抽取器做兜底；极端情况下纯 cgroup fallback |
| Agent 可能"谎报" predictions（为了骗 reward） | Matcher 硬核验时间/路径，编造会匹配失败 | RL reward 天然惩罚不实声明 |
| 纯内存操作（reflective loading, rowhammer） | sensor 看不见，Matcher 无事件可匹配 | cgroup 兜底标 attack；事件缺失记 limitation |
| eBPF-aware rootkit 加载后 | sensor 可能失明 | 加载事件仍捕获；"post-load 降级"记在 manifest |
| Agent 时间推断不准（predict 60s, 实际 120s） | time_window 不够宽会漏匹配 | time_window 设默认 ±50%；或 agent 学着用更宽窗 |
| 多 session 同机器同 IP | cgroup 依然能分开，但 matcher 时间对齐可能混淆 | step_id 唯一性保证 |

### 7.9 研究展望：agent × detector 对偶演化（数据飞轮）

这是 sysbox 作为研究平台的**核心长期价值**。

```
┌──────────────────────────────────────────────────────────────┐
│   Agent RL 训练                    Detector 训练              │
│   ┌────────────────────┐          ┌────────────────────┐    │
│   │ Input: objective   │          │ Input: events      │    │
│   │ Output: trajectory │          │ Output: TTP / alert│    │
│   │   + predictions    │          │                    │    │
│   │                    │          │                    │    │
│   │ Reward:            │          │ Supervision:       │    │
│   │  - prediction 准度 │          │  - labeled events  │    │
│   │    (来自 matcher)  │          │    (来自 sysbox)   │    │
│   │  - objective 达成  │          │                    │    │
│   │  - 避开 detector   │◄──────┐  │                    │    │
│   │    (对抗)          │       │  │                    │    │
│   └─────────┬──────────┘       │  └─────────┬──────────┘    │
│             │                  │            │                │
│             │ 更强的 agent     │            │ 更强的 detector│
│             ▼                  │            ▼                │
│   ┌────────────────────────────┴────────────────────────┐    │
│   │          sysbox 平台                                │    │
│   │     field + sensor + matcher + labeler              │    │
│   └──────────────────────┬──────────────────────────────┘    │
│                          │                                   │
│                      共用数据                                │
│                    (labeled events +                         │
│                    match reports +                           │
│                    trajectory)                               │
│                          │                                   │
│                          └─── 下一轮迭代                     │
└──────────────────────────────────────────────────────────────┘
```

**飞轮的四个良性反馈：**

1. **Agent 预测更准 → 标签更细**：early stage agent 预测 60% matched，随训练进步到 95%，label 覆盖率同步提升
2. **标签更细 → Detector 更强**：detector 训练数据质量提升，泛化能力增强
3. **Detector 更强 → 反过来给 agent 做对抗信号**：agent 某步声称成功但 detector 看到 alert，训练时扣分；反之声称隐蔽但 detector 检测到，也扣分
4. **Agent 为避开 detector 学更隐蔽的 TTP → 新 TTP 进数据集**：数据多样性持续扩大，detector 被迫泛化

**这是 sysbox 超越 "dataset 生成工具" 的地方——它是红蓝自进化 platform**。Dataset 不再是一次性产出，而是连续升级的 artifact。

**对研究 novelty 的意义：**

- DSec：提供 sandbox infra，不涉及 detection
- DARPA TC：人工 red team 一次性产出，没有自进化
- sysbox：**同一平台、同一数据、agent 和 detector 对偶演化**——据我所知是新的

这条线远期可以支撑多篇论文：
- "sysbox: 一个 agentic APT 研究平台"（平台论文）
- "基于 prediction-matching 的自监督 GT 标注"（方法论文）
- "使用 RL 训练 covert APT agent"（攻击侧论文）
- "从对抗数据训练 APT detector"（防御侧论文）
- "Agent × Detector 对偶演化研究"（综合论文）

---

## 8. 观测管线实现

### 8.1 Sensor 部署拓扑

```
sysbox-host
  ├─ sysbox-sensor-collector (per-run daemon)
  │    持有 cgroup_id→session_id 映射、跨节点传播表
  │
  ├─ host-ebpf-sensor (tracee 子进程)
  │    覆盖所有 Docker 容器（共享宿主内核）
  │
  └─ per-guest sensor (microVM/VM 内)
       烘在 rootfs 里，systemd 拉起
       virtio-serial 回传事件给 collector
```

### 8.2 Run 目录 layout

```
runs/<run_id>/
├── state.json                     # Terraform-style state
├── topology.hcl                   # HCL 输入快照
├── field-manifest.json
│
├── events/
│   └── raw-<hour>.jsonl.zst       # 原始事件，按小时切
│
├── sessions/
│   └── session-<id>/
│       ├── cgroup-members.jsonl
│       ├── process-tree.json
│       └── entry.json
│
├── trajectory/
│   └── session-<id>.jsonl         # 实验层 dump
│
└── labeled/                       # dataset label 产出
    ├── labeled-events.jsonl.zst
    ├── provenance-graph-<id>.json
    ├── match-report.json           # 新增: RL reward 原料
    └── manifest.json
```

### 8.3 确定性保证

Labeler 纯函数：同样 `events + predictions + matcher config` → 字节级一致 labeled 输出。

实现要点：
- 迭代 sorted key，不依赖 map 顺序
- 并发结果 sort.Slice 后合并
- 时间戳从事件读，不调 time.Now()
- UUID 从内容 hash 派生

### 8.4 Replay bundle

```
sysbox replay bundle <run_id>
  → runs/<run_id>/replay-bundle.tar.zst

内含:
  topology.hcl, images 清单, events/ raw, trajectory.jsonl (含 predictions),
  labeled/, matcher-config.yaml (time window / strict-loose mode),
  sysbox-version, REPRODUCE.md

别人:
  sysbox replay extract bundle.tar.zst
  sysbox dataset label ./extracted --matcher-config matcher-config.yaml
  → 字节级一致的 labeled-events + match-report 输出
```

---

## 9. 开源组件依赖

### 9.1 核心 Go 库

| 目的 | 库 |
|---|---|
| HCL 解析 | `github.com/hashicorp/hcl/v2` + `github.com/zclconf/go-cty` |
| Provider plugin | `github.com/hashicorp/go-plugin` |
| Docker | `github.com/docker/docker/client` |
| Firecracker | `github.com/firecracker-microvm/firecracker-go-sdk` |
| libvirt | `github.com/digitalocean/go-libvirt` |
| netlink | `github.com/vishvananda/netlink` + `vishvananda/netns` |
| nftables | `github.com/google/nftables` |
| Containerlab（当库用） | `github.com/srl-labs/containerlab` |
| eBPF | `github.com/cilium/ebpf` |
| MCP（可选，供 experiment 层参考） | `github.com/mark3labs/mcp-go` |

### 9.2 外部二进制

| 用途 | 工具 |
|---|---|
| Host eBPF sensor | Tracee (aquasecurity/tracee) |
| Guest sensor | 自写 Go 二进制 (~500 行) |

### 9.3 不自己造的东西

- **Agent trajectory 记录**：Langfuse/LangSmith/Phoenix/OTEL GenAI
- **Agent 本身**：Claude Code / OpenAI Agents SDK / 用户自选
- **配置语言**：HCL v2
- **网络原语**：containerlab 代码
- **Provider 协议**：go-plugin

---

## 10. MVP 范围与分期

### 10.1 v1.0 MVP（4 个月 × 2 人）

**必须有：**

**平台与拓扑**
- HCL v2 parser + resource graph + state file
- `init / plan / apply / destroy / state / show / output` CLI
- Docker / Firecracker / libvirt 三 substrate providers
- linux-bridge 网络 provider（含 firewall、router、ssh_access）

**观测**
- Tracee sensor（宿主机 eBPF）
- 自写 sysbox-guest-sensor（microVM/VM 内，~500 行 Go + virtio-serial）
- Sensor collector daemon（per-run，cgroup_id ↔ session_id 映射 + 跨节点传播）

**Session 机制**
- cgroup v2 session enforcement
- sshd ForceCommand wrapper + 预制 `sysbox/session-enabled-base` 基础镜像
- `sysbox session register` CLI

**Prediction / Matcher / Labeler（核心）**
- Prediction Schema 定义（7 种基础 effect type）
- Python SDK: `sysbox.session.declare_step(intent=..., predicted_effects=[...])`
- CoT-to-Prediction 自动抽取器（路径 B，~50 行 Python + LLM 调用）
- Matcher：双流 JOIN 算法实现（~500 行 Go，取代之前计划的 1500 行规则引擎）
- Cgroup 兜底标签
- Match Report 产出

**输出产物**
- Labeled JSONL (per-event)
- Provenance graph JSON (per-session)
- Match Report JSON（RL reward 原料）
- Dataset manifest + realism scorecard
- Replay bundle

**验证**
- `labeler validate --benign-baseline` (零假阳)
- 10-20 个内置 scenario（每个附带手工 predictions 作为 ground truth）
- `labeler validate --prediction-quality`
- `labeler validate --against-darpa-tc` 交叉验证脚本

**明确推迟（v2.0+）：**
- 多机 sysbox-runtimed
- OVS network type
- SysArmor sensor implementation
- Falco / auditd / custom sensor
- Kafka / OTLP sink
- CUE frontend
- K8s operator wrapper
- Windows guest 观测（ETW/Sysmon）
- 硬件级 / VMI 观测
- Agent RL training pipeline（sysbox 只产出 Match Report；实际 RL 训练留给下游研究）

### 10.2 四期规划

| Phase | 周次 | 里程碑 |
|---|---|---|
| 1 | W1-4 | **Hello World field**：HCL parser + Docker provider + linux-bridge + 基本 CLI。能 `apply` 起 2 个 Alpine 容器连通 |
| 2 | W5-8 | **观测与 session 锚定**：Tracee 集成 + cgroup v2 + sshd wrapper + cgroup 兜底标注。能 SSH 进容器跑命令，产出带 `is_attack` 的 JSONL |
| 3 | W9-12 | **Prediction Matcher + 异构 substrate**：Firecracker + libvirt + guest-sensor + 跨节点传播 + Prediction Schema + Matcher + Match Report。能让 Claude Code 跑完整攻击链并输出 step-level 标签 |
| 4 | W13-16 | **发布级 dataset**：10-20 scenario 验证套件 + Replay bundle + realism scorecard + prediction-quality 验证 + 文档 + examples |

### 10.3 Done 标准

- 新机器 clone→install→apply→destroy 一键打通
- Benign baseline 24h 运行 attack_events=0
- 10/10 内置 scenario（含手工 predictions）F1 ≥ 0.90
- 同 events+predictions → 字节级一致 labels 和 match report
- Match Report 可直接喂给样例 RL 训练脚本（小型 demo）
- Replay bundle 离线分发后能完整复现
- 所有 provider 有 e2e 集成测试

### 10.4 Labeler 工程量对比

旧设计（7 规则引擎）：~1500 行 Go
新设计（Prediction Schema + Matcher + Cgroup 兜底）：~500 行 Go + ~200 行 Python SDK

**新设计不仅工程量小，还更直接表达业务**——matcher 逻辑可以直接对着 prediction schema 读懂，而规则引擎的 7 条规则需要维护文档解释。

---

## 11. 与其他工具的对比

| 维度 | sysbox | containerlab | DSec | DARPA TC |
|---|---|---|---|---|
| 声明式拓扑 | ✓ HCL | ✓ YAML | ✗ | ✗ |
| Container | ✓ | ✓ | ✓ | — |
| microVM | ✓ Firecracker | ✗ | ✓ | — |
| Full VM | ✓ libvirt | 部分(vrnetlab) | ✓ | 物理 |
| 多节点异构同图 | ✓ | ✗ (只容器) | — | — |
| 内置 eBPF 观测 | ✓ | ✗ | — | ✓ (手搭) |
| Session 归属 | ✓ cgroup v2 内核锚定 | ✗ | ✓ (trajectory-only) | 人工 red team |
| GT 标注 | ✓ prediction × event 对齐 | ✗ | ✗ | 人工打标 |
| Match Report / RL 信号 | ✓ | ✗ | 部分 (trajectory-only) | ✗ |
| 红蓝对偶演化支持 | ✓ | ✗ | ✗ | ✗ |
| Replay bundle | ✓ | ✗ | — | — |
| 开源 | v1 起即开源 | ✓ | ✗ 闭源 | 免费但不维护 |

---

## 12. 风险与开放问题

### 12.1 已识别风险

| 风险 | 严重 | 缓解 |
|---|---|---|
| sshd wrapper 配置不兼容用户镜像 | 中 | 提供 `sysbox/session-enabled-base` 官方镜像 + 文档 |
| cgroup v2 未启用的老 Linux | 低 | 检测 + 明确错误提示（Ubuntu 22.04+ 默认都开） |
| 跨节点 session 传播实时性不足 | 中 | collector 响应时间做监控；事件缓冲+追溯兜底 |
| Agent 试图逃逸 cgroup（CAP_SYS_ADMIN） | 低 | 逃逸行为本身被观测为 attack 事件；标注数据受益 |
| 内核 rootkit 致 eBPF 失明 | 中 | 加载事件已捕获；post-rootkit 事件降级处理 |
| 硬件级反虚拟化 | 低 | 非 sysbox scope，记入 limitations |
| **Agent 模型小或不配合 → 产出不了 prediction** | 中 | 路径 B 自动抽取器用 LLM 从 CoT 抽预测；极端情况下 cgroup 完全兜底 |
| **Agent 故意瞎编 prediction（骗 reward）** | 低 | Matcher 硬核验，编造→匹配失败→扣 reward，RL 自然惩罚 |
| **Prediction Schema 不够表达某些 TTP** | 中 | Schema 按 YAML 承载易扩展；社区可贡献新 effect type |
| **Matcher 时间窗过窄漏匹配 / 过宽错匹配** | 中 | 默认时间窗 ±50%；提供 `--strict-window` / `--loose-window` 切换用于消融 |
| **多 session 并发时 prediction 混淆** | 低 | step_id 全局唯一；matcher 只在 session cgroup 内匹配 |
| **自动抽取器（LLM-based）不稳定** | 中 | Abstract 工作走离线 batch；异常结果打 flag 人工 review |

### 12.2 未决问题（留给 implementation plan 阶段）

- 跨节点 session 传播的 collector→guest-sensor 指令通道协议细节
- sshd wrapper 用 ForceCommand 还是 PAM module（两者都有优劣）
- Firecracker rootfs 构建 pipeline（自建 vs 依赖 Alpine/Debian 镜像）
- Guest-sensor 对宿主 collector 的 heartbeat/backoff 策略
- Matcher 在内存里构图还是流式处理（取决于 event 规模 + prediction 数量）
- Prediction Schema 的版本管理（schema 演进时历史 dataset 怎么办）
- sysbox_router 的实现：单独 provider？还是 sysbox_node + 自动渲染？
- CoT 自动抽取用本地 LLM 还是 API？（考虑成本和复现性）

---

## 13. 附录

### 13.1 术语表

- **Field**：sysbox apply 之后活着的拓扑 + 观测栈整体
- **Substrate**：隔离基底（docker/firecracker/libvirt）
- **Node**：拓扑里的一个机器（容器/microVM/VM 三选一）
- **Session**：一次 agent 接入事件对应的 attack 归属标签
- **Ground Truth (GT)**：每个事件是 attack/benign 的权威标注
- **Trajectory**：agent 的思维/工具调用记录（外部 AI 可观测性栈负责）
- **Prediction Schema**：Agent 声明每步预期效果的结构化格式（type + required/optional fields + time_window）
- **Predicted Effect**：Agent 每 step 附带的一条预测，描述"我期望看到什么样的 sensor 事件"
- **Matcher**：Sysbox labeler 的核心算法——把 predictions 和 events 做双流 JOIN
- **Match Report**：Matcher 产出的 per-step 匹配统计，作为 RL reward 原料
- **Cgroup 兜底**：未匹配上 prediction 的 cgroup 成员事件仍标 is_attack=true，但无 intent 标签
- **Data Flywheel**：Agent 预测能力、标签质量、Detector 能力三者互相提升的正反馈循环

### 13.2 参考

- SysField overview: `sysbox/references/docs/sysfield-overvew.md`
- DeepSeek DSec: `sysbox/references/docs/deepseek-ecs.md`
- SysArmor: `sysbox/references/docs/sysarmor-overview.md`
- ACP protocol: `sysbox/references/docs/acp-protocal.md`
- OpenTelemetry GenAI semantic conventions
- MITRE ATT&CK Matrix
- DARPA Transparent Computing (TC) 数据集
- Atomic Red Team 场景库

---

*本文档由 brainstorming 对话产出，内容稳定后将转入 implementation plan。*
