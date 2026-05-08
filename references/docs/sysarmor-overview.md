# Agent

Agent 是 SysArmor 的端侧守护进程，运行在受保护主机上，做四件事：采集内核事件、端侧检测告警、上传遥测数据、执行响应命令。

---

## 模块概览

```
┌──────────────────────────────────────────────────────────────────┐
│                        sysarmor-agent                            │
│                                                                   │
│  ┌────────────┐  RawEvent   ┌─────────────────────────────────┐  │
│  │ SysSensor  │ ──────────► │           SysEngine             │  │
│  │  (eBPF)    │             │  ProvenanceSketch  PatternEngine │  │
│  └────────────┘             │  WAL 写入          响应执行      │  │
│                             └───────────────┬───────────────────┘  │
│                                             │ WAL (bbolt)          │
│                                             ▼                      │
│  ┌──────────────────────────────────────────────────────────────┐ │
│  │  SysRelay                                                    │ │
│  │  HeartbeatLoop  ←→  ServerMessage 流（规则/命令/证书下发）   │ │
│  │  walEventUploadLoop  WAL → gRPC UploadEvents / ReportAction  │ │
│  └──────────────────────────────────────────────────────────────┘ │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────────┐ │
│  │  SysPlugin                                                   │ │
│  │  子进程生命周期管理（shipper 模式：直接 HTTP POST 到 Relay）  │ │
│  └──────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

---

## 1. SysSensor（eBPF 采集层）

### 内核挂载点

Agent 加载 5 组 eBPF 程序，覆盖进程、文件、网络、内核操作四类行为：

| 程序组 | 挂载方式 | 覆盖范围 |
|--------|---------|---------|
| **ExecTracer** | tracepoint | `execve`, `execveat`, `sched_process_fork`, `sched_process_exit` |
| **FileTracer** | tracepoint | `openat`, `unlinkat`, `renameat2`, `chmod/fchmodat`, `fchownat`, `linkat`, `truncate`, `symlinkat`, `mount` |
| **NetTracer** | tracepoint | `connect`, `accept4`, `bind`, `sendto`, `socket` |
| **ProcTracer** | tracepoint | `ptrace`, `setuid/setgid`, `init_module/delete_module`, `mmap`, `mprotect`, `process_vm_writev`, `memfd_create`, `clone`（NS 标志）, `unshare`, `setns`, `capset`, `prctl` |
| **LSMTracer** | BPF LSM hook | `task_kill`, `file_open`, `bprm_check_security`, `socket_connect` |

LSMTracer 要求 Linux 5.7+，内核启用 `CONFIG_BPF_LSM=y` 并配置 `lsm=bpf`。LSM hook 可以**阻断**操作（不只是观测），Agent 用它保护自身二进制文件不被篡改、阻断被隔离进程的 fork/exec。

### RawEvent

所有采集到的原始事件统一封装为 `RawEvent`：

```
RawEvent {
  Kind:      ExecKind | ForkKind | ExitKind | FileKind | NetKind | ProcKind | ExecveatKind
  Timestamp: 内核单调时钟（ktime_get_ns，ns since boot）
  PID / PPID / UID / GID
  Comm:      内核 task_comm（最多 15 字节）
  Data:      根据 Kind 携带对应结构体
}
```

**ExecData**（ExecKind / ExecveatKind）

| 字段 | 说明 |
|------|------|
| `Filename` | 可执行文件完整路径 |
| `Argv` | 命令行参数列表 |
| `Inode` | 可执行文件 inode 号 |
| `ExeHash` | SHA-256（用户态异步计算，可能为空） |
| `DirFD` / `AtFlags` | 仅 ExecveatKind 有意义；`AT_EMPTY_PATH=0x1000` 是 memfd 无文件执行特征 |

**ForkData**（ForkKind）：`ChildPID uint32`

**ExitData**（ExitKind）：`ExitCode int32`

**FileData**（FileKind）

| 字段 | 说明 |
|------|------|
| `Path` / `Path2` | 操作路径；rename 时 Path2 为新路径，symlink 时为链接名 |
| `Op` | FileOpen / FileUnlink / FileRename / FileChmod / FileWrite / FileSymlink / FileMount / FileChown / FileLink / FileTruncate |
| `Flags` / `Mode` | open flags；chmod 时为新权限；fchown 时为 new_uid/new_gid |
| `Inode` | 文件 inode 号 |

**NetData**（NetKind）

| 字段 | 说明 |
|------|------|
| `Op` | NetConnect / NetAccept / NetBind / NetDNS / NetConnectResult / NetSocket |
| `DstIP` / `DstPort` | 目标地址 |
| `Domain` | DNS 查询名称（NetDNS） |
| `ConnectResult` | syscall 返回值（NetConnectResult，0=成功，负值=errno） |
| `SocketDomain` / `SocketType` | socket() 创建 raw/packet 套接字时的地址族和类型 |

**ProcData**（ProcKind）

| 字段 | 说明 |
|------|------|
| `Op` | ProcPtrace / ProcSetUID / ProcModLoad / ProcMmapRWX / ProcProcWrite / ProcMemfd / ProcMprotect / ProcCloneNS / ProcMmapX / ProcUnshare / ProcSetns / ProcSetGID / ProcCapset / ProcPrctl / ProcModUnload |
| `TargetPID` | ptrace / process_vm_writev 的目标进程 |
| `NewUID` / `Addr` / `ProtFlags` / `ModName` | 各 Op 的附加参数 |

### BPF Maps（内核-用户态控制通道）

| Map | 用途 |
|-----|------|
| `blocked_pids` | LSM 阻断：被标记进程无法 fork/exec |
| `blocked_inodes` | LSM 阻断：禁止执行指定 inode |
| `blocked_ips` | LSM 阻断：禁止连接指定 IP |
| `protected_inodes` | LSM 保护：禁止写打开指定 inode（Agent 自保护） |
| `tier_ctrl` | 运行时开关各层事件采集（bitmask） |
| `sa_event_config` | FileTracer 文件子类型过滤 |

---

## 2. SysEngine（检测引擎）

SysEngine 从 SysSensor 读取 RawEvent，并发执行两条路径：

1. **上传路径**：每条事件富化上下文后序列化写入 WAL，由上传循环批量推送 Relay
2. **检测路径**：每条事件送入 PatternEngine 做规则匹配，命中则立即执行响应动作并上报告警

### ProvenanceSketch（进程溯源）

eBPF 只能看到单条事件，无法知道"这个进程是 bash 的后代"。ProvenanceSketch 在用户态维护一棵进程树，让检测规则可以断言祖先链条件。

**内存结构：**

```
nodes:     (PID, StartTimeNs) → ProcessNode   // 复合键区分 PID 复用
activePID: PID → StartTimeNs                  // 快速查当前活跃进程
rings:     PID → eventRing                    // per-PID 最近 N 条事件，供 sequence 规则查询
activity:  ActivityID → []PID                 // fork 链成员集合，供跨进程关联查询
```

**状态更新触发：**

| 事件 | 动作 |
|------|------|
| `ForkKind` | 新建子进程节点，从父节点**继承** ActivityID |
| `ExecKind` / `ExecveatKind` | 更新节点的 exe / comm；节点不存在时自动创建 |
| `ExitKind` | 从 activePID 移除；节点保留供异步消费 |

**ActivityID** 标识一条 fork 链（格式 `AC-{fnv32(pid||startNs):08x}`）：

- Fork 时子进程直接继承父节点的 ActivityID，整条攻击链共享同一 ID
- 父节点未见的孤立进程以自身 `(pid, startNs)` 为种子生成，后续子进程仍继承
- 相同进程实例的 ID 不随时间变化（不依赖时间桶）

```
bash (PID 200)  ActivityID: AC-aabb1122
  └─ sh (PID 300)                AC-aabb1122
       └─ python (PID 400)       AC-aabb1122
            └─ /tmp/dropper      AC-aabb1122  ← 整条链共享
```

启动时 `ScanProcFS()` 预扫描 `/proc`，将已运行进程注册到 Sketch，确保 Agent 启动后的第一批事件就能获得完整祖先链上下文。

### PatternEngine（内联 IOA 检测）

PatternEngine 使用自研的 **PatternRule** 格式（YAML），在主机本地完成 IOA 规则匹配，无需将原始事件传出即可触发告警和响应。

> PatternRule 是自研格式，不是 Sigma。Sigma 规则由云端 Prism 执行，是两套独立的检测机制。

**规则格式：**

```yaml
id: exec-from-tmp
title: Execute Binary from /tmp
severity: high
score: 0.9

trigger:
  kinds: [exec]           # 只对指定 Kind 求值；可列多个

match:                    # AND 语义：所有条件全部满足才继续
  - field: process.executable
    op: startswith
    value: /tmp/

ancestor:                 # 祖先链：任意一层满足所有谓词即通过
  - field: comm
    op: eq
    value: bash

exclude:                  # NOT 语义：满足则丢弃
  - field: process.parent.pid
    op: eq
    value: "1"

response:                 # 命中后自动执行的本地响应
  actions: [kill-process]
```

**五阶段匹配（任一阶段不满足则短路跳过）：**

```
Phase 1: trigger — 事件 Kind 不匹配直接跳过（快速路径）
Phase 2: match   — 从 RawEvent 扁平化提取字段，AND 匹配所有条件
Phase 3: ancestor — sketch.Ancestors(pid, depth) 向上遍历，任意一层满足所有谓词即通过
Phase 4: sequence — 在 per-PID ring 或 activity 中查找满足时间窗口的历史事件序列
Phase 5: exclude  — 满足任一条件则丢弃
```

**可用字段（`extractFields` 输出）：**

| 类别 | 字段名 |
|------|--------|
| 进程 | `process.pid`, `process.parent.pid`, `process.uid`, `process.gid`, `process.comm`, `process.executable`, `process.name`, `process.inode`, `process.args`, `process.argv0`, `process.pgid`, `process.sid`, `process.execveat.dirfd`, `process.execveat.at_flags` |
| 文件 | `file.path`, `file.name`, `file.op`, `file.inode`, `file.flags`, `file.mode`, `file.target_path`, `file.mount.source`, `file.mount.flags` |
| 网络 | `network.op`, `destination.ip`, `destination.port`, `network.domain`, `network.connect_result`, `network.socket_domain`, `network.socket_type` |
| 内核操作 | `proc.op`, `proc.target_pid`, `proc.mod_name`, `proc.addr`, `proc.new_uid`, `proc.new_gid`, `proc.ns_type`, `proc.prot_flags`, `proc.cap_effective` |
| 关联 | `activity.id` |
| 祖先链专用 | `comm`, `exe`, `uid`, `pid`（仅在 `ancestor` 块内有效） |

**操作符：**

| 操作符 | 含义 |
|--------|------|
| `eq` / `neq` | 等于 / 不等于 |
| `in` / `not_in` | 值在列表中（配合 `values:` 字段） |
| `contains` / `not_contains` | 包含子串 |
| `startswith` / `endswith` | 前缀 / 后缀 |
| `wildcard` | 通配符（`*` 和 `?`） |
| `re` | 正则表达式 |
| `gt` / `lt` / `gte` / `lte` | 数值比较 |
| `cidr` | IP 属于 CIDR 段 |

**响应动作（本地执行，BPF map 同步更新防重启）：**

| 动作 | 实现 |
|------|------|
| `kill-process` | SIGKILL + 写入 `blocked_pids` |
| `quarantine-file` | chmod 000 + 记录保护路径 |
| `isolate-network` | 写入 `blocked_ips`，LSM 阻断后续连接 |
| `block-process` | 写入 `blocked_inodes`，LSM 阻断执行 |

---

## 3. SysRelay（gRPC 通信层）

### gRPC 接口

```protobuf
service AgentService {
  rpc Enroll(EnrollRequest)              returns (EnrollResponse);
  rpc Register(RegisterRequest)          returns (RegisterResponse);
  rpc Heartbeat(stream HeartbeatRequest) returns (stream ServerMessage);
  rpc UploadEvents(stream EventsBatch)   returns (UploadResponse);
  rpc ReportAction(ActionResult)         returns (Empty);
}
```

### 心跳流（HeartbeatLoop）

Agent 每 30s 发送一次 `HeartbeatRequest`，同一条双向流接收服务端下发的 `ServerMessage`。

**上行（Agent → Relay）：**

| 字段 | 说明 |
|------|------|
| `status` | HEALTHY / DEGRADED / ERROR |
| `ruleset_version` | 当前已加载的规则版本 |
| `resources` | Agent 进程 RSS、goroutine 数、主机 CPU%、内存、负载、网络 IO |
| `data_flow.events_dropped` | eBPF perf buffer ring overflow 累计丢弃数 |

**下行（Relay → Agent，ServerMessage）：**

| 类型 | 用途 |
|------|------|
| `HeartbeatAck` | 确认心跳；携带规则集增量（`RulesetUpdate`）和配置补丁（`PolicyDelta`） |
| `ServerCommand` | 响应命令列表（kill-process / quarantine-file / isolate-network） |
| `CertRotation` | 新证书 PEM + 生效时间戳 |
| `TaskDispatch` | 批量任务下发（handler 待实现，当前仅记录日志） |
| `Notification` | 服务端通知（如 `upgrade_available`） |

### 上传循环（walEventUploadLoop）

单一 Timer 驱动的上传循环（每 1s 一次），从 WAL 按 topic 批量读取并上传：

| WAL 主题 | 批大小 | 目标 |
|----------|--------|------|
| `sysarmor.event.endpoint` | ≤500 条 | gRPC `UploadEvents`（DATA_TYPE_EVENT）→ Kafka |
| `sysarmor.event.process` | ≤500 条 | gRPC `UploadEvents`（DATA_TYPE_EVENT）→ Kafka |
| `sysarmor.alert.endpoint` | ≤500 条 | gRPC `UploadEvents`（DATA_TYPE_EVENT）→ Kafka |
| `sysarmor.result.action` | 逐条 | `ReportAction` RPC（每条最多 3 次，0s/1s/2s 退避） |

所有 topic 全部成功才推进 WAL 游标；任意一个失败则下次 tick 整批重传（at-least-once）。

`sysarmor.result.action` 是内部 WAL 路由键，**不是 Kafka 主题**。云端命令执行结果和 PatternEngine 自动响应结果都写入此 topic，经由 `ReportAction` RPC 直接上报 Relay，Relay 写入 `execution_results` 表。

### WAL

WAL（Write-Ahead Log）用 bbolt（B+ 树，单文件嵌入式 KV）实现断网持久化。全局单序列，按写入时间顺序存储所有 topic 的条目。

- `NoSync=true`：提高写入吞吐（OS 负责最终落盘）
- `maxSizeMB`：达到上限后丢弃最旧条目，保护磁盘
- 启动恢复：从 bucket 最后一个 key 还原 `seqNum`，cursor 从 0 开始，自动跳过已 Ack 的条目

### mTLS 证书体系

```
CA（由 Manager/Relay 持有）
 ├─ Relay 服务端证书
 └─ Agent 客户端证书（每台主机独立，Enroll 时签发）
     CN=<agent_id>, SAN=<machine_id>
```

首次注册（Enroll）时提交一次性 `enrollment_token`，Relay 验证后签发 `client_cert + key + ca_cert`，写入 `data_dir/certs/`。后续所有 RPC 均使用 mTLS 双向认证。证书到期前，Relay 通过心跳流下发 `CertRotation` 触发无缝轮换。

---

## 4. SysPlugin（外部插件）

SysPlugin 管理以独立子进程运行的第三方工具（如 Tracee）。插件只支持 **shipper 模式**：子进程直接 HTTP POST 数据到 Relay `:6752/agentless/upload`，Agent 仅负责进程生命周期，不参与数据中转。

插件二进制由部署侧提供；Agent 不内置任何插件。

### 生命周期管理

- `RestartPolicy`：`always` / `on-failure`（默认）/ `never`，最多重启 `MaxRestarts` 次（默认 5）
- **cgroup v2 隔离**：在 `/sys/fs/cgroup/plugin-<name>/` 创建子 cgroup，限制 CPU% 和内存；创建失败时打 warn 日志继续运行

### plugins.yaml

```yaml
version: "1"
plugins:
  - name: tracee
    binary: /usr/bin/tracee
    args: ["--output", "json"]
    env:
      RELAY_URL: "https://relay:6752"
      RELAY_AGENTLESS_TOKEN: "xxx"
    cgroup_cpu_pct: 20
    cgroup_mem_mb: 256
    restart_policy: on-failure
    max_restarts: 5
```

| 字段 | 说明 |
|------|------|
| `name` | 日志标识符 |
| `binary` | 可执行文件绝对路径 |
| `args` | 额外命令行参数 |
| `env` | 注入的环境变量（通常含 `RELAY_URL` / `RELAY_AGENTLESS_TOKEN`） |
| `restart_policy` | `always` / `on-failure` / `never` |
| `max_restarts` | 最大重启次数（0 = 5） |
| `cgroup_cpu_pct` | CPU 上限，百分比（0 = 不限） |
| `cgroup_mem_mb` | 内存上限，MiB（0 = 不限） |
| `config` | 通过 `PLUGIN_CONFIG` 环境变量注入的透明 JSON 配置 |

---

## 5. 启动流程

```
main()
 ├─ 加载配置（/etc/sysarmor/agent.yaml）
 ├─ [首次] Enroll：提交 enrollment_token，获取 agent_id + 客户端证书
 ├─ Connect：建立 mTLS gRPC 连接（Relay :8443）
 ├─ Register：上报主机基本信息（hostname / IP / OS / CPU / 内存 / 内核版本）
 ├─ engine.SetActionReporter(relay.ActionReporter())
 │       将响应结果上报路径（WAL-first）注入检测引擎
 ├─ sensor.ProtectInode(agent_binary_inode)
 │       将 Agent 自身可执行文件写入 protected_inodes BPF map（best-effort）
 └─ 并发启动（各 goroutine 均以 loopWithBackoff 指数退避重启）
     ├─ sensor.Start()          — 加载 eBPF，启动 perf buffer drain
     ├─ engine.Start(events())  — ProvenanceSketch + WAL 写入 + PatternEngine
     ├─ plugin.Start(ctx)       — 启动插件子进程
     ├─ relay.RunHeartbeat()    — 双向心跳流
     └─ relay.RunUpload()       — WAL 上传循环
```

---

## 相关文档

- [数据管道详解](agent-pipeline.md) — 单条事件从内核到 Kafka 的完整旅程
- [数据流](data-flow.md) — 从主机到 OpenSearch 的端到端路径
- [控制流](control-flow.md) — 注册、心跳、命令下发、规则同步


---

# Agent 内部管道

本文讲一件事：**一条内核事件，如何从 eBPF 走到 Kafka**。读完后你应该清楚每个阶段做了什么、为什么这样设计。

---

## 全程一图

```
内核 syscall / LSM hook
  │ eBPF tracepoint 捕获
  ▼
perf buffer（内核↔用户态共享环形缓冲）
  │ SysSensor.drain()
  ▼
rawEvents channel
  │ engine.runFeedLoop()
  │
  ├─ [Fork/Exec/Exit] ──► ProvenanceSketch 更新进程树
  │
  ├─ [所有事件] ─────────► sk.Enrich() 附加 ActivityID / ancestor_chain / 挂钟时间戳
  │                           │
  │                           ▼
  │                        WAL.Write()──────────────────────┐
  │                         ├─ sysarmor.event.endpoint      │
  │                         └─ sysarmor.event.process       │  上传路径
  │                                                         │
  └─ [所有事件] ─────────► PatternEngine 五阶段规则匹配      │
                               │                            │
                          命中 → *Verdict                   │
                               │                            │
                          liftVerdicts()                    │
                            ├─ 立即执行响应动作              │
                            │   (kill/quarantine/isolate)   │
                            ├─ WAL.Write("alert.endpoint") ─┤
                            └─ WAL.Write("result.action") ──┘
                                                            │
                                      walEventUploadLoop (1s Timer)
                                            │
                          ┌─────────────────┼──────────────────────┐
                          │                 │                      │
                    event.endpoint   alert.endpoint          result.action
                    event.process         │                        │
                          │        gRPC UploadEvents         ReportAction RPC
                   gRPC UploadEvents      │                  (3次退避重传)
                          │               │                        │
                          └───────────────┴──────── Relay :8443 ──┘
                                                        │
                                                   Kafka Topics
```

---

## 第一步：内核采集 → RawEvent

eBPF 程序挂载在内核 tracepoint 和 BPF LSM hook 上，当 `execve`、`openat`、`connect` 等系统调用发生时，eBPF 把原始参数写入 **perf buffer**（内核与用户态共享的环形内存区）。

`SysSensor.drain()` goroutine 持续从 perf buffer 读取字节流，解码成结构化的 `RawEvent`，投入 Go channel：

```
RawEvent {
  Kind:      ExecKind | ForkKind | ExitKind | FileKind | NetKind | ProcKind | ExecveatKind
  Timestamp: 内核单调时钟（ktime_get_ns，ns since boot，非 Unix epoch）
  PID / PPID / UID / GID / Comm
  Data:      各 Kind 对应的结构体（ExecData / ForkData / FileData / ...）
}
```

**此阶段的局限：** eBPF 只能看到当前这一条事件，不知道这个进程的父进程是谁、它在哪条攻击链上。这是下一步要解决的问题。

---

## 第二步：ProvenanceSketch —— 建立进程家谱

光有 RawEvent 不够用。检测规则需要问"这个进程的祖先是不是 bash？"，告警需要携带攻击链信息。所以 `engine.runFeedLoop` 在处理每条事件前，先用 **ProvenanceSketch** 维护一棵进程树：

```
nodes:     (PID, StartTimeNs) → ProcessNode   // 复合键，防止 PID 复用混淆
activePID: PID → StartTimeNs                  // 快速查当前活跃进程
rings:     PID → eventRing                    // per-PID 近期事件，供 sequence 规则查询
activity:  ActivityID → []PID                 // fork 链成员，供跨进程关联查询
```

只有三种 Kind 会改变树结构：

| 事件 | 动作 |
|------|------|
| `ForkKind` | 新建子节点，从父节点**继承** ActivityID |
| `ExecKind` / `ExecveatKind` | 更新节点的 exe / comm；节点不存在时自动注册 |
| `ExitKind` | 从 activePID 移除；节点保留供异步消费 |

**ActivityID** 是攻击链的唯一标识，格式 `AC-{fnv32(pid||startNs):08x}`。Fork 时子进程直接继承父节点的 ActivityID，整条攻击链共享同一个 ID：

```
bash (PID 200)   AC-aabb1122
  └─ sh (300)    AC-aabb1122  ← 继承
       └─ python (400)  AC-aabb1122  ← 继承
            └─ /tmp/payload (500)  AC-aabb1122  ← 继承
```

孤立进程（父节点未见过）以自身 `(pid, startNs)` 为种子生成 ActivityID，后续子进程仍继承。

**启动时预扫描：** `engine.Start()` 最先调用 `sk.ScanProcFS()`，把 `/proc` 中所有当前运行的进程注册进树。这确保 Agent 启动后第一批事件就能拿到完整的祖先链，而不会因为进程树为空而丢失上下文。

---

## 第三步：Enrich —— 给每个事件附加上下文

进程树更新后，**所有 7 种 Kind 的事件**都经过 `sk.Enrich(raw)`，产出 `Telemetry`：

| 附加字段 | 来源 | 说明 |
|---------|------|------|
| `ActivityID` | 进程节点 | 整条攻击链共享的 ID |
| `ProcessExe` | 进程节点 ExePath | 任意 Kind 均可填充 `process.executable` |
| `EventWallTimeNs` | `raw.Timestamp + bootOffsetNs` | BPF 单调时钟转为 Unix 挂钟时间 |
| `AncestorChain` | `sk.Ancestors(pid, 10)` | **仅 exec 事件携带**（最多 10 层，只含 pid/comm/exe） |
| `AgentID` / `Hostname` | Agent 配置 | 标识来源主机 |

**为什么 ancestor_chain 只在 exec 事件携带？** file/net/proc 事件数量远多于 exec，若每条都附上祖先链会显著增大 Kafka 消息体积。下游 Flink 用 `activity_id` 做 keyed-state join 即可拿到进程上下文，不需要每条事件重复携带。

**为什么需要修正时间戳？** BPF 内核侧调用 `ktime_get_ns()` 返回的是系统单调时间（距上次开机的纳秒数），不是 Unix epoch。Agent 启动时计算 `bootOffsetNs = wall_ns - ktime_ns`，Enrich 时加上此偏移得到正确的挂钟时间，OpenSearch 才能正常索引。

---

## 第四步：两条并行路径

`runFeedLoop` 拿到 `Telemetry` 后同时走两条路径，互不阻塞：

### 路径 A：上传路径

```
Telemetry
  │ serialize() → ECS JSON
  ▼
WAL.Write("sysarmor.event.endpoint")   ← 所有 7 种 Kind
WAL.Write("sysarmor.event.process")    ← 仅 fork/exec/exit（精简格式）
```

直接写 WAL，无中间 channel。WAL 用 bbolt（单文件 B+ 树）实现，写入即持久化——即使进程崩溃，事件也不会丢。

### 路径 B：检测路径

```
RawEvent（不经 JSON 序列化）
  │ rawCh（typed Go channel）
  ▼
PatternEngine.Processor
  │ 五阶段匹配：trigger → match → ancestor → sequence → exclude
  │ ancestor 查询调用 sk.Ancestors(pid, 64)（64 层，比上传深度 10 高得多）
  │
  ├─ 未命中 → 忽略
  └─ 命中 → *Verdict → verdictCh → liftVerdicts goroutine
                                        │
                              ┌─────────┴──────────┐
                         响应动作                WAL 告警
                    （立即本地执行）        WAL.Write("alert.endpoint")
                    kill / quarantine       WAL.Write("result.action")
                    isolate / block
```

**响应动作立即执行**，不等 WAL 确认：安全响应不能有延迟。执行结果随后写入 WAL（`sysarmor.result.action`），由上传循环异步上报。

---

## 第五步：WAL 上传循环

`walEventUploadLoop` 是一个 Timer 驱动的循环（每 1 秒一次），从 WAL 按写入时间顺序读取条目，按 topic 分组上传：

| WAL 主题 | 批大小 | 上报方式 |
|----------|--------|---------|
| `sysarmor.event.endpoint` | ≤500 条 | gRPC `UploadEvents` |
| `sysarmor.event.process` | ≤500 条 | gRPC `UploadEvents` |
| `sysarmor.alert.endpoint` | ≤500 条 | gRPC `UploadEvents` |
| `sysarmor.result.action` | 逐条 | `ReportAction` RPC（最多 3 次，0s/1s/2s 退避） |

**游标推进规则：** 所有 topic 全部成功 → 推进游标，Ack 这批条目；任意一个失败 → 游标不动，下次 tick 整批重传（at-least-once 语义）。

**断网恢复：** 重启后游标从 0 开始，自动跳过已 Ack 的条目，从最后一个成功位置继续上传。WAL 设有容量上限（`maxSizeMB`），超出后丢弃最旧条目，防止磁盘耗尽。

---

## Kafka 事件格式

Relay 收到 `EventsBatch` 后，根据 `batch.topic` 字段原样写入对应 Kafka 主题。

### sysarmor.event.endpoint（exec 事件，含祖先链）

```json
{
  "@timestamp": "2026-05-03T10:23:45.123Z",
  "event.kind":     "event",
  "event.dataset":  "sysarmor.exec",
  "event.category": ["process"],
  "event.type":     ["start"],
  "host.hostname":  "web-server-01",
  "process.pid":          500,
  "process.parent.pid":   400,
  "process.executable":   "/tmp/payload",
  "process.name":         "payload",
  "process.args":         ["/tmp/payload"],
  "sysarmor.activity_id": "AC-aabb1122",
  "sysarmor.agent_id":    "uuid-agent",
  "sysarmor.ancestor_chain": [
    {"pid": 400, "comm": "python3", "exe": "/usr/bin/python3"},
    {"pid": 300, "comm": "sh",      "exe": "/bin/sh"},
    {"pid": 200, "comm": "bash",    "exe": "/bin/bash"}
  ]
}
```

### sysarmor.event.endpoint（file 事件，无祖先链）

```json
{
  "@timestamp": "2026-05-03T10:23:45.200Z",
  "event.kind":     "event",
  "event.dataset":  "sysarmor.file.open",
  "event.category": ["file"],
  "event.type":     ["access"],
  "process.pid":         500,
  "process.executable":  "/tmp/payload",
  "file.path":    "/etc/passwd",
  "file.name":    "passwd",
  "sysarmor.activity_id": "AC-aabb1122",
  "sysarmor.agent_id":    "uuid-agent"
}
```

### sysarmor.event.process（进程生命周期，精简格式）

```json
{
  "@timestamp": "2026-05-03T10:23:44.900Z",
  "event.kind":    "event",
  "event.action":  "process_start",
  "process.pid":         500,
  "process.parent.pid":  400,
  "process.executable":  "/tmp/payload",
  "process.name":        "payload",
  "sysarmor.activity_id": "AC-aabb1122",
  "sysarmor.agent_id":    "uuid-agent"
}
```

---

## 相关文档

- [Agent 模块设计](agent.md) — eBPF 挂载点、PatternRule 格式、SysRelay 接口、SysPlugin
- [数据流](data-flow.md) — 从主机到 OpenSearch 的端到端路径
- [控制流](control-flow.md) — 注册、心跳、命令下发、规则同步

---

# 数据流

本文讲一件事：**安全数据从哪里来、经过什么处理、最终存在哪里**。

---

## 全程一图

```
         ┌── Agent 模式 ──────────────────────────┐
         │ eBPF → PatternEngine → WAL             │
         │                    gRPC mTLS :8443      │
         └────────────────────────────────────────┘
                       │
         ┌── Agentless 模式 ─────────────────────┐
         │ auditd / Tracee                        │
         │               HTTP Bearer :6752        │
         └────────────────────────────────────────┘
                       │
                    Relay
                       │
                    Kafka
               ┌───────┴────────────────────┐
           事件流 (7d)                  告警流 (30d)
               │                            │
        ┌──────┴──────┐             ┌───────┴───────┐
      Flink          Prism        Flink           Prism
   (流处理/关联)   (Sigma 检测)  (规则匹配)    (AlertCollector)
        │                             │                │
        └─────────────────────────────┴────────────────┘
                                      │
                              OpenSearch 告警索引
                          sysarmor-alerts-YYYY.MM
                                      ▲
                            Manager REST API 查询
```

---

## 路径 A：Agent 模式

Agent 通过 eBPF 采集内核事件，PatternEngine 在端侧做第一道检测，结果经 WAL 缓冲后通过 gRPC mTLS 上传到 Relay。

```
内核 eBPF
  │ perf buffer → rawEvents channel
  ▼
SysEngine (ProvenanceSketch + Enrich)
  │ ECS JSON 序列化
  ▼
WAL (bbolt)
  │ walEventUploadLoop (1s)
  ▼
gRPC UploadEvents → Relay :8443
  ├─ sysarmor.event.endpoint  → Kafka  (全量 ECS 遥测，7d)
  ├─ sysarmor.event.process   → Kafka  (进程生命周期，7d)
  └─ sysarmor.alert.endpoint  → Kafka  (PatternEngine 告警，30d)

PatternEngine 命中时还会额外写：
  sysarmor.result.action  → ReportAction RPC → Relay → Postgres execution_results
  （响应执行结果，不经过 Kafka）
```

**事件格式（ECS core + `sysarmor.*` 扩展）：**

```json
{
  "@timestamp": "2026-05-03T10:23:45.123Z",
  "event.kind":     "event",
  "event.dataset":  "sysarmor.exec",
  "event.category": ["process"],
  "event.type":     ["start"],
  "host.hostname":  "web-server-01",
  "process.pid":          500,
  "process.parent.pid":   400,
  "process.executable":   "/tmp/payload",
  "sysarmor.activity_id": "AC-aabb1122",
  "sysarmor.agent_id":    "uuid-agent",
  "sysarmor.ancestor_chain": [
    {"pid": 400, "comm": "python3", "exe": "/usr/bin/python3"},
    {"pid": 300, "comm": "sh",      "exe": "/bin/sh"}
  ]
}
```

---

## 路径 B：Agentless 模式

没有安装 Agent 的主机，通过 rsyslog / Fluent Bit 将 auditd 日志，或通过 Tracee 容器 HTTP POST 到 Relay，经 Flink 完成标准化与检测。

```
auditd + rsyslog
Tracee eBPF          POST :6752/agentless/upload (Bearer Token)
                              │
                           Relay
                              │
                    sysarmor.raw.audit (Kafka, 3d)
                              │
                    Flink normalize-audit
                              │
                    sysarmor.event.audit (Kafka, 7d)
                              │
                    Flink detect-falco-audit
                              │
                    sysarmor.alert.audit (Kafka, 30d)
```

> Tracee 当前与 auditd 共用 `sysarmor.raw.audit`，`sysarmor.raw.tracee` 主题已预留但 Relay 尚未按 `source_mode` 分流。

---

## 告警汇聚

所有来源的告警，无论是 Agent 端侧检测、Flink 流处理检测，还是 Prism 服务端 Sigma 检测，最终都由 **Prism AlertCollector** 统一写入 OpenSearch：

```
sysarmor.alert.endpoint   ─┐
sysarmor.alert.prism      ─┤ Prism AlertCollector → OpenSearch
sysarmor.alert.audit      ─┤ （滑动窗口去重，抑制告警风暴）
sysarmor.alert.correlation─┘

索引: sysarmor-alerts-YYYY.MM（每月滚动）
查询: Manager GET /api/v2/alerts → 直接读 OpenSearch
```

告警不写 Postgres。Prism 做去重：相同 `agent_id:rule_id` 在滑动窗口内只写一次。

---

## Flink 关联检测

Flink 在事件流和告警流之间做跨主机关联，输出三类关联告警：

| 作业 | 输入 | 输出 |
|------|------|------|
| `correlate-lateral-movement` | `event.endpoint` + `alert.endpoint` | `alert.correlation` |
| `correlate-ip-association` | `event.endpoint` | `alert.correlation` |
| `correlate-alert-chain` | `alert.*` | `alert.correlation` |

关联告警同样经 Prism AlertCollector 写入 OpenSearch。

---

## Kafka 主题目录

### 原始数据（3 天保留）

| 主题 | 来源 |
|------|------|
| `sysarmor.raw.audit` | Relay Agentless（auditd + Tracee） |
| `sysarmor.raw.unknown` | Relay 解析失败回退 |

### 标准化事件（7 天保留）

| 主题 | 格式 | 来源 |
|------|------|------|
| `sysarmor.event.endpoint` | ECS core + `sysarmor.*` | Agent（全量，7 种 Kind） |
| `sysarmor.event.process` | 精简进程格式 | Agent（仅 fork/exec/exit） |
| `sysarmor.event.audit` | Falco/sysdig 字段格式 | Flink normalize-audit |

### 告警（30 天保留）

| 主题 | 来源 |
|------|------|
| `sysarmor.alert.endpoint` | Agent PatternEngine |
| `sysarmor.alert.prism` | Prism Sigma 检测 |
| `sysarmor.alert.audit` | Flink detect-falco-audit |
| `sysarmor.alert.correlation` | Flink correlate-* |

### 其他

| 主题 | 用途 |
|------|------|
| `sysarmor.asset.snapshot` | Agent 资产快照（processes / ports / packages） |
| `sysarmor.flink.rules` | Prism RuleSync 发布的规则快照（Flink 热加载） |

> 响应执行结果（ActionResult）不经过 Kafka，由 Agent 通过 `ReportAction` RPC 直接上报 Relay，写入 Postgres `execution_results` 表。

---

## 压缩与背压

| 环节 | 压缩 |
|------|------|
| Agent → Relay（事件流） | zstd level 3（`EventsBatch.payload`） |
| Relay → Manager（资产） | zstd（HTTP body） |
| OpenSearch 内部 | LZ4（块级透明） |

背压机制：
- **Relay 限速**：`UploadResponse.throttle_ms`，Agent 在批次间 sleep 对应时长
- **WAL 容量保护**：达到 `maxSizeMB` 后丢弃最旧事件，防止磁盘耗尽
- **Prism 去重**：滑动窗口内同 `agent_id:rule_id` 只写一次 OpenSearch
- **Flink 并行度**：按 Kafka 分区数扩展，`event.endpoint` 32 分区为最高并行度

---

## 相关文档

- [Agent 内部管道](agent-pipeline.md) — 单条事件从内核到 Kafka 的完整旅程
- [控制流](control-flow.md) — 注册、心跳、命令下发、规则同步
- [Agent 模块设计](agent.md) — eBPF、PatternEngine、WAL、SysPlugin
