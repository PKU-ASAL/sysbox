# sysbox Phase 2 — Observation & Session Anchor

> **Goal:** 让 sysbox 能观测在 field 里运行的**所有**进程，用 cgroup v2 把 SSH 入口的进程子树锚定到 session，输出带 `session_id` + `is_attack` 标注的事件 JSONL。Phase 2 结束时能验证一条完整路径：SSH 进容器跑 nmap → 事件 `session_id != ""`，`is_attack = true`；非 SSH 进程（node-init 守护 / docker exec / 模拟 webshell）→ 事件 `session_id = ""`，`is_attack = false`（等 Phase 3 跨节点传播 + Matcher 做精细归属）。

**Architecture:**
```
sysbox apply field.hcl
  └─ sensor subprocess (per node, Tracee)
       ├─ Event Reader (消费 tracee JSON)
       ├─ cgroup-based Labeler
       │     cgroup_id → session_id（内核强制继承，只信 cgroup）
       ├─ cgroup v2 session enforcement (SSH ForceCommand wrapper)
       ├─ Process Tree Builder (内部辅助结构, 供 Phase 3 Matcher 查询)
       └─ JSONL sink → /runs/<id>/events.jsonl
```

**核心约束**（方案 A，严格遵循原 design doc Section 6/7.1）：
- session 归属**只来自 cgroup 成员身份**。非 cgroup 成员的事件 `session_id = ""`，Phase 2 不推断
- ProcessTreeBuilder 维护 pid→ancestry 表，但**不导出到事件 schema**，只作 Phase 3 Matcher 的查询 API
- 平台不内嵌启发式判断（如 `entry_point = "webshell"`）；intent/TTP 归类是 Phase 3 Matcher 的职责
- Phase 2 事件 schema 最小化：`node_id / session_id / pid / ppid / ts / type / name / args / is_attack`

**Tech additions:**
- `github.com/aquasecurity/tracee/pkg/...` — syscall/event capture via eBPF（或调用 tracee binary）
- `github.com/opencontainers/runc/libcontainer/cgroups` — cgroup v2 操作
- 自制 `sysbox-sshd-hook` 二进制（ForceCommand wrapper）

**Out of scope (Phase 3+):** Prediction Matcher, 跨节点 session 传播, HTTP request-level session 归属, Firecracker/libvirt, replay bundle。

---

## 文件结构增量（Phase 2 结束时）

```
sysbox/
├── pkg/
│   ├── sensor/
│   │   ├── sensor.go          # Sensor interface (Start/Stop/Events)
│   │   ├── tracee.go          # TraceeBackend: 启动 tracee 进程，解析 JSON 事件流
│   │   ├── proctree.go        # ProcessTreeBuilder: pid→ancestry map，消费 fork/execve
│   │   └── sensor_test.go
│   ├── session/
│   │   ├── session.go         # Session 数据结构 (ID, NodeID, User, StartTime, CgroupID)
│   │   ├── cgroup.go          # cgroup v2: 新建 session cgroup, 迁移 PID, 读 cgroup_id
│   │   ├── labeler.go         # 单层 Labeler: cgroup_id → session_id（无 fallback）
│   │   ├── registry.go        # SessionRegistry: pre-registered expectation (Task 4b)
│   │   ├── registry_test.go
│   │   └── session_test.go
│   └── sink/
│       ├── sink.go            # EventSink interface
│       └── jsonl.go           # JSONL file sink with session_id annotation
│
├── pkg/provider/docker/
│   └── ssh.go                 # SSHAccess: inject sysbox-sshd-hook into container
│
├── cmd/
│   ├── sysbox/commands/
│   │   ├── sensor_cmd.go              # sysbox sensor start/stop/status
│   │   ├── session_cmd.go             # sysbox session list / attach
│   │   └── session_register_cmd.go    # sysbox session register (Task 4b)
│   └── sysbox-sshd-hook/
│       └── main.go            # ForceCommand wrapper: resolves session from registry or generates UUID, creates cgroup
│
├── pkg/runtime/
│   └── executor.go            # extend createNode to call sensor.Start after StartNode
│                              # extend createSSHAccess (实施 sysbox_ssh_access)
│
└── tests/e2e/
    └── sensor_test.go         # SSH in, run nmap, assert events with is_attack=true
```

---

## Task 1: Sensor interface + Tracee backend

**Files:**
- Create: `pkg/sensor/sensor.go`
- Create: `pkg/sensor/tracee.go`
- Create: `pkg/sensor/sensor_test.go`

### Step 1: 定义 Sensor 接口

```go
package sensor

import "context"

// Event is a normalized observation from inside a node.
//
// Schema is intentionally minimal: Phase 2 only annotates session_id (via cgroup)
// and a cgroup-fallback is_attack flag. Intent/TTP classification is Phase 3's job.
type Event struct {
    NodeID    string         `json:"node_id"`
    SessionID string         `json:"session_id,omitempty"` // non-empty iff event's cgroup_id matches a session
    CgroupID  uint64         `json:"cgroup_id"`            // kernel-reported, raw (scrubbed before dataset export)
    Timestamp int64          `json:"ts"`                   // unix nano
    PID       int            `json:"pid"`
    PPID      int            `json:"ppid"`
    Type      string         `json:"type"`                 // "syscall" | "net" | "file"
    Name      string         `json:"name"`                 // e.g. "execve"
    Args      map[string]any `json:"args"`
    IsAttack  bool           `json:"is_attack"`            // true iff SessionID != ""
}

// Sensor observes a running node.
type Sensor interface {
    // Start begins observation. Events are written to the returned channel.
    Start(ctx context.Context, nodeID string, containerPID int) (<-chan Event, error)
    // Stop gracefully shuts down the sensor.
    Stop() error
    // ProcessTree returns the internal ProcessTreeBuilder for Phase 3 Matcher
    // queries. Not exposed on events.
    ProcessTree() *ProcessTreeBuilder
}
```

**不在 schema 里的东西（有意）：**
- `process_tree` / `ancestry`：由 ProcessTreeBuilder 内部维护，Phase 3 Matcher 通过 `sensor.ProcessTree()` 按需查询，**不附在每条事件上**。理由：避免事件体积膨胀、保留 schema 简洁、让 Phase 3 标注决定是否查树。
- `entry_point` enum（"ssh" / "webshell" / "exec" / "node-init"）：这是启发式分类，属于 Phase 3 Matcher 的先验；Phase 2 不在平台层做判断。

### Step 2: Tracee backend（调用 tracee binary）

```go
// TraceeBackend forks `tracee` with --filter container.id=<containerID>
// and reads its JSON event stream from stdout.
type TraceeBackend struct {
    Traceebin string // path to tracee binary
    cmd       *exec.Cmd
    events    chan Event
}
```

- `Start()`: 检查 tracee binary 存在，`exec.Cmd` 启动，goroutine 逐行解析 JSON，写入 `events` chan
- `Stop()`: `cmd.Process.Kill()`
- 测试：mock tracee binary，输入预制 JSON，验证 Event 解析

### Step 3: Process Tree Builder（`pkg/sensor/proctree.go`）—— **内部辅助结构**

这是 sensor 内部维护的 pid→ancestry 映射，**不进入事件 schema**。Phase 3 Matcher 需要查进程祖先链时，通过 `sensor.ProcessTree().Ancestry(pid)` 按需查询。

Tracee 的 raw 事件里每条都带 `pid` / `ppid` / `comm`。ProcessTreeBuilder 消费这些事件维护内存表：

```go
type ProcInfo struct {
    Comm string
    PPID int
}

type ProcessTreeBuilder struct {
    mu    sync.RWMutex
    procs map[int]ProcInfo // pid → info
}

// Feed 消费一条 raw Tracee 事件，更新内部进程表。
// 对 clone/fork/execve 事件特别处理：
//   - fork/clone: 新增子进程条目 (childPid → {comm=parentComm, ppid=parentPid})
//   - execve:     更新 comm 为新镜像名
func (b *ProcessTreeBuilder) Feed(raw map[string]any)

// Ancestry 返回 pid 的祖先链（从 pid=1 的容器 init 到当前 pid），
// 以 comm 列表表示，例如 ["sleep","sshd","bash","nmap"]。
// 查不到的节点用 "?" 占位，避免因事件乱序导致链断。
//
// Phase 2 内部 API: 测试 + Phase 3 Matcher 使用；事件 schema 不导出。
func (b *ProcessTreeBuilder) Ancestry(pid int) []string
```

**约束：**
- 进程树表只在内存里；sensor 重启后从空开始（Phase 3 可持久化到 BoltDB）
- Tracee 启动时先接收已有进程的 `existing_process` 事件预热进程表
- **不在 sensor 内做任何启发式分类**（entry_point、webshell、node-init 之类）。那是 Phase 3 Matcher 的活

### Step 4: 集成到 executor.createNode

在 `StartNode` 后，若 `NodeSpec` 带 `sensor: true`（新字段）或 HCL 里 `sysbox_node` 有 `sensor = true`，自动启动 TraceeBackend 并把 chan 路由到 sink。

---

## Task 2: cgroup v2 Session Enforcement

**Files:**
- Create: `pkg/session/cgroup.go`
- Create: `pkg/session/session.go`
- Create: `pkg/session/session_test.go`

### Step 1: cgroup 操作

```go
// CreateSessionCgroup creates /sys/fs/cgroup/sysbox/<nodeID>/<sessionID>/
// and returns its cgroup_id (read from cgroup.stat or /proc/self/cgroup).
func CreateSessionCgroup(nodeID, sessionID string) (uint64, error)

// MoveProcess moves pid to the session cgroup.
func MoveProcess(nodeID, sessionID string, pid int) error

// DeleteSessionCgroup removes the cgroup when the session ends.
func DeleteSessionCgroup(nodeID, sessionID string) error
```

使用 `os.MkdirAll` + `os.WriteFile("/sys/fs/cgroup/.../cgroup.procs", ...)` 直接操作 cgroupfs，不依赖 libcgroup 或 runc。

### Step 2: Session 数据结构

```go
type Session struct {
    ID         string    `json:"id"`
    NodeID     string    `json:"node_id"`
    User       string    `json:"user"`
    CgroupID   uint64    `json:"cgroup_id"`
    StartTime  time.Time `json:"start_time"`
    EndTime    *time.Time `json:"end_time,omitempty"`
}
```

### Step 3: 单层 cgroup Labeler

Labeler 只做一件事：**查 `cgroup_id → session_id` 映射表，命中则打 session 标签**。非命中的事件 `session_id=""`、`is_attack=false`。

```go
// Labeler 严格按 cgroup 成员身份归属 session。不做进程树溯源、不做启发式推断。
type Labeler struct {
    mu          sync.RWMutex
    cgroupTable map[uint64]string // cgroup_id → session_id
}

// RegisterSession 在 session cgroup 建立时调用（由 sysbox-sshd-hook 或
// sysbox session register CLI 通知）。
func (l *Labeler) RegisterSession(cgroupID uint64, sessionID string)

// UnregisterSession 在 session 结束时清表。
func (l *Labeler) UnregisterSession(cgroupID uint64)

// Annotate 填充 Event 的 SessionID 和 IsAttack 字段。
//   - cgroupTable[event.CgroupID] 命中 → SessionID = <id>, IsAttack = true
//   - 否则                         → SessionID = "",   IsAttack = false
func (l *Labeler) Annotate(e *sensor.Event)
```

**为什么非 session cgroup 事件 is_attack=false：** 原设计 Section 6.1 的 Layer A 约定，"cgroup 成员身份是永不错标的底层真实"，`is_attack=true` 严格对应 session cgroup 命中。webshell、node-init 守护、docker exec 等非 session 进程产生的事件，Phase 2 标 `is_attack=false`；它们的真正归属由 **Phase 3 的跨节点传播 + Matcher** 决定（届时 cgroup 可能被实时建立为 sub-session，或由 Matcher 把事件关联到某个 agent prediction step）。

**容器 pid=1 怎么处理：** 不特殊对待。apply 时不预注入任何 "node-init" 根节点；容器启动后 pid=1 就是 `sleep infinity`，ProcessTreeBuilder 看到后自然记录。不需要为它单独建 cgroup 或打标签。

---

## Task 3: sysbox-sshd-hook（ForceCommand wrapper）

**Files:**
- Create: `cmd/sysbox-sshd-hook/main.go`

### 设计

SSH daemon 配置 `ForceCommand /usr/local/bin/sysbox-sshd-hook` 时，sshd 在执行用户 shell 前先执行 hook。Hook 流程：

```
1. 从环境变量读 SSH_CONNECTION, SSH_ORIGINAL_COMMAND, SYSBOX_NODE_ID
2. 生成 session_id（nanoid 或 uuid）
3. 调用 sysbox-sshd-hook API（Unix socket 或 env var）通知 sysbox 主进程
4. 创建 session cgroup，把当前 PID 移进去
5. exec 用户 shell 或 SSH_ORIGINAL_COMMAND
```

### 构建

```makefile
build-hook:
    go build -o bin/sysbox-sshd-hook ./cmd/sysbox-sshd-hook
```

---

## Task 4: sysbox_ssh_access 实施

**Files:**
- Create: `pkg/provider/docker/ssh.go`
- Modify: `pkg/runtime/executor.go`（实施 createSSHAccess）

### SSH Access 流程

1. 进入容器安装 openssh-server（`apk add openssh`）
2. 注入 `sysbox-sshd-hook` binary（CopyToNode）
3. 写 `/etc/ssh/sshd_config.d/sysbox.conf`：
   ```
   ForceCommand /usr/local/bin/sysbox-sshd-hook
   AuthorizedKeysFile /etc/sysbox/authorized_keys
   ```
4. 写 authorized_keys（来自 HCL `authorized_keys`）
5. 启动 sshd

### HCL 示例

```hcl
resource "sysbox_ssh_access" "admin" {
  node            = sysbox_node.target.id
  authorized_keys = ["ssh-ed25519 AAAA... user@host"]
  port            = 2222
}
```

---

## Task 4b: `sysbox session register` CLI

**Files:**
- Create: `cmd/sysbox/commands/session_register_cmd.go`
- Modify: `pkg/session/session.go`（加 SessionRegistry + 跨进程通信）

**为什么需要：** 原设计 Section 6.2 约定，实验层在 agent 接入**之前**调 `sysbox session register` 声明预期 session（node + source IP + 期望的 session_id）。sshd-hook 通过 sysbox 主进程查询该表，把预先声明的 session_id 用到 cgroup 上，而不是让 hook 自己生成 UUID。

这一机制在 Phase 2 支持"实验层控制 session_id"的基本用法；Phase 3 会扩展成跨节点传播的通道（collector → guest-sensor 推送期望）。

### CLI 形态

```bash
sysbox session register \
    --node target \
    --source 10.0.1.1 \      # 期望的 SSH 源 IP（可选, 默认匹配任意源）
    --session-id exp-abc \   # 外部 trace id（通常来自 Langfuse/OTEL）
    --expires-in 60s         # 过期时间（超时未匹配则删除）
```

### 数据结构

```go
// pkg/session/registry.go
type Expectation struct {
    NodeID    string
    SourceIP  string     // "" = 任意源
    SessionID string
    ExpiresAt time.Time
}

type Registry struct {
    mu      sync.RWMutex
    entries map[string]*Expectation // key = nodeID+":"+sourceIP
}

// Resolve 被 sysbox-sshd-hook 调用，查询给定 (node, source) 对应的预期 session_id。
// 未命中返回空串（hook 自己生成 UUID 兜底）。
func (r *Registry) Resolve(nodeID, sourceIP string) string

// Register 由 CLI 命令写入；可能通过 unix socket 或文件 (runs/<id>/session-registry.json) 持久化。
func (r *Registry) Register(exp Expectation) error
```

**Phase 2 实现简化：** Registry 用本地文件 `runs/<runID>/session-registry.json`，`Register` 追加，`Resolve` 读文件并过滤过期项。不引入 daemon / socket。

### Step 1: 实现 Registry 基础结构

`pkg/session/registry.go`:

```go
package session

import (
    "encoding/json"
    "os"
    "sync"
    "time"
)

type Expectation struct {
    NodeID    string    `json:"node_id"`
    SourceIP  string    `json:"source_ip,omitempty"`
    SessionID string    `json:"session_id"`
    ExpiresAt time.Time `json:"expires_at"`
}

type Registry struct {
    path string
    mu   sync.Mutex
}

func NewRegistry(path string) *Registry { return &Registry{path: path} }

func (r *Registry) Register(exp Expectation) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    existing, _ := r.loadLocked()
    existing = append(existing, exp)
    return r.saveLocked(existing)
}

func (r *Registry) Resolve(nodeID, sourceIP string) string {
    r.mu.Lock()
    defer r.mu.Unlock()
    entries, err := r.loadLocked()
    if err != nil {
        return ""
    }
    now := time.Now()
    for _, e := range entries {
        if e.NodeID != nodeID {
            continue
        }
        if e.SourceIP != "" && e.SourceIP != sourceIP {
            continue
        }
        if now.After(e.ExpiresAt) {
            continue
        }
        return e.SessionID
    }
    return ""
}

func (r *Registry) loadLocked() ([]Expectation, error) {
    data, err := os.ReadFile(r.path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil, nil
        }
        return nil, err
    }
    var out []Expectation
    return out, json.Unmarshal(data, &out)
}

func (r *Registry) saveLocked(entries []Expectation) error {
    data, _ := json.MarshalIndent(entries, "", "  ")
    return os.WriteFile(r.path, data, 0o644)
}
```

### Step 2: CLI 命令

`cmd/sysbox/commands/session_register_cmd.go`:

```go
package commands

import (
    "fmt"
    "path/filepath"
    "time"

    "github.com/oslab/sysbox/pkg/session"
    "github.com/spf13/cobra"
)

var (
    sessRegNode      string
    sessRegSource    string
    sessRegID        string
    sessRegExpiresIn string
)

var sessionRegisterCmd = &cobra.Command{
    Use:   "register",
    Short: "Pre-register a session expectation; sshd-hook will use its session_id",
    RunE: func(cmd *cobra.Command, args []string) error {
        dur, err := time.ParseDuration(sessRegExpiresIn)
        if err != nil {
            return fmt.Errorf("parse --expires-in: %w", err)
        }
        exp := session.Expectation{
            NodeID:    sessRegNode,
            SourceIP:  sessRegSource,
            SessionID: sessRegID,
            ExpiresAt: time.Now().Add(dur),
        }
        regPath := filepath.Join(filepath.Dir(flagStateFile), "session-registry.json")
        reg := session.NewRegistry(regPath)
        if err := reg.Register(exp); err != nil {
            return err
        }
        fmt.Printf("Registered: %s → %s (expires %s)\n", sessRegNode, sessRegID, exp.ExpiresAt.Format(time.RFC3339))
        return nil
    },
}

func init() {
    sessionRegisterCmd.Flags().StringVar(&sessRegNode, "node", "", "target node name")
    sessionRegisterCmd.Flags().StringVar(&sessRegSource, "source", "", "source IP (optional; matches any if empty)")
    sessionRegisterCmd.Flags().StringVar(&sessRegID, "session-id", "", "session ID (typically from Langfuse/OTEL)")
    sessionRegisterCmd.Flags().StringVar(&sessRegExpiresIn, "expires-in", "60s", "expiration window")
    _ = sessionRegisterCmd.MarkFlagRequired("node")
    _ = sessionRegisterCmd.MarkFlagRequired("session-id")
}
```

该命令挂在 `session` 子命令组下（和 `session list` 一起）：

```go
// 在 session_cmd.go 里:
sessionCmd.AddCommand(sessionListCmd, sessionRegisterCmd)
```

### Step 3: sshd-hook 消费 Registry

修改 `cmd/sysbox-sshd-hook/main.go`：hook 启动时查 Registry，若命中则用预期 session_id，否则生成 UUID：

```go
regPath := os.Getenv("SYSBOX_REGISTRY_PATH") // 由 container 启动时注入
reg := session.NewRegistry(regPath)

sourceIP := extractSourceIP(os.Getenv("SSH_CONNECTION"))
nodeID := os.Getenv("SYSBOX_NODE_ID")

sessionID := reg.Resolve(nodeID, sourceIP)
if sessionID == "" {
    sessionID = generateUUID()
}
```

### 测试

- Unit test `pkg/session/registry_test.go`: Register/Resolve/expiration 边界
- Integration test（在 Task 7 E2E 里）：`sysbox session register --session-id exp-abc ...` 然后 SSH 进去，验证事件的 `session_id == "exp-abc"`（不是随机 UUID）

---

## Task 5: Event JSONL Sink

**Files:**
- Create: `pkg/sink/sink.go`
- Create: `pkg/sink/jsonl.go`

```go
type EventSink interface {
    Write(e sensor.Event) error
    Close() error
}

// JSONLSink appends events as newline-delimited JSON.
type JSONLSink struct {
    path string
    f    *os.File
    enc  *json.Encoder
}
```

事件文件路径：`runs/<runID>/events.jsonl`，运行中可 `tail -f` 实时观察。

---

## Task 6: sensor CLI subcommands

**Files:**
- Create: `cmd/sysbox/commands/sensor_cmd.go`

```
sysbox sensor start    # 对 state 中所有 node 启动 sensor（需 root）
sysbox sensor stop     # 停止所有 sensor
sysbox sensor status   # 每个 node sensor 的运行状态
sysbox session list    # 列出所有 session（读 runs/<id>/sessions.json）
```

---

## Task 7: E2E tests — SSH session 锚定

**Files:**
- Create: `tests/e2e/sensor_test.go`

三个 E2E 测试，全部验证 cgroup 为唯一 session 锚定机制：

### TestSensorSSHSession（核心路径）

```
1. apply hello-world field (with sysbox_ssh_access)
2. sysbox sensor start
3. SSH into node_a: run "nmap 127.0.0.1"
4. sleep 2s
5. parse runs/.../events.jsonl
6. Assert: execve(nmap) event has
     session_id != ""            // 在 SSH session cgroup 里
     is_attack == true
     cgroup_id != 0
7. destroy
```

### TestSensorRegisteredSessionID（验证 Task 4b）

```
1. apply hello-world field (with sysbox_ssh_access)
2. sysbox sensor start
3. sysbox session register --node node_a --session-id exp-abc --expires-in 60s
4. SSH into node_a: run "whoami"
5. sleep 2s
6. parse runs/.../events.jsonl
7. Assert: execve(whoami) event has
     session_id == "exp-abc"     // 预先注册的 id，不是随机 UUID
     is_attack == true
8. destroy
```

### TestSensorNonSessionEvents（cgroup 兜底的严格语义）

```
1. apply hello-world field（无 ssh_access）
2. sysbox sensor start
3. 不建任何 session；触发一些非 session 活动：
     docker exec sysbox-node_a ls /etc        // docker exec 不经 sshd-hook
4. sleep 2s
5. parse events.jsonl
6. Assert: execve(ls) event has
     session_id == ""            // 没进 session cgroup
     is_attack == false          // Phase 2 严格语义: 只有 session 成员才是 attack
7. destroy
```

**为什么没有 TestSensorWebshellTree：** 原设计把 webshell 归属（跨节点 TCP 传播 → 目标节点建 sub-cgroup）放在 Phase 3。Phase 2 的正确行为就是让 webshell 事件落在 `session_id=""` 的桶里，不做启发式分类。这个桶由 Phase 3 的跨节点 collector + Matcher 按 agent prediction 回填。

**process tree 不在事件 schema 里了**——E2E 不 assert `process_tree`。若需要在测试里读进程祖先链（用于调试），可以通过 sensor 的内部 API（`sensor.ProcessTree().Ancestry(pid)`）按需查询，但不作为事件字段 assert。

---

## Phase 2 完成检查清单

**SSH session 路径（核心）：**
- [ ] `sysbox apply field.hcl` + `sysbox sensor start` 无报错
- [ ] `sysbox_ssh_access` 实施完整：sshd 就绪、authorized_keys 生效、ForceCommand 指向 `sysbox-sshd-hook`
- [ ] SSH 进容器，hook 创建 session cgroup，把入口 PID 移入；`cat /proc/self/cgroup` 能看到 `sysbox.slice/<node>/<session>/`
- [ ] 运行 nmap，在 events.jsonl 里看到 `session_id != ""`, `is_attack = true`
- [ ] `sysbox session list` 显示活跃 session
- [ ] `sysbox sensor stop` 后事件停止写入

**`sysbox session register` 路径：**
- [ ] `sysbox session register --session-id exp-abc` 后 SSH 进入，事件 `session_id == "exp-abc"`
- [ ] 未注册时，hook fallback 生成 UUID session_id
- [ ] 过期的注册不会被 Resolve 命中

**cgroup 兜底严格语义：**
- [ ] 非 session 的进程（docker exec、容器自启动守护）事件 `session_id == ""`, `is_attack == false`
- [ ] 不存在 `entry_point` / `process_tree` / 任何其他启发式分类字段（event schema 严格最小化）
- [ ] ProcessTreeBuilder 作为 sensor 内部结构可通过 `sensor.ProcessTree()` 查询，但**不**出现在事件流里

**E2E：**
- [ ] TestSensorSSHSession PASS
- [ ] TestSensorRegisteredSessionID PASS
- [ ] TestSensorNonSessionEvents PASS

---

## Phase 2 结束后，Phase 3 起步提示

Phase 3 的核心工作是**Prediction Matcher + 跨节点 session 传播 + Firecracker/libvirt substrates**。具体来说：

- **跨节点 session 传播**（解决 webshell 归属）：Phase 2 里 session 只在 SSH 入口节点生效；Phase 3 加一个 collector daemon，监听 session 内进程的出站 TCP（`connect()` 事件），提前在目标节点的 guest-sensor 里注册 "期望 X 秒内接收来自这个 IP 的入口"。目标节点的 sshd-hook / accept() 处理器命中后，在目标节点本地建 sub-cgroup 把新衍生进程塞进去——session 跨节点延续，webshell 自然有 session_id。
- **Prediction Matcher**：Phase 2 事件是 "有 cgroup 归属就 `is_attack=true`"。Phase 3 加第二层标注：agent 每步声明 `predicted_effects`，Matcher 机械地把 events 和 predictions JOIN，匹配上的事件继承 agent 的 intent/TTP。ProcessTreeBuilder（Phase 2 内部 API）在这一步会被 Matcher 查询。
- **Match Report**：Matcher 的副产物，每 step 的 prediction 命中率 → 直接作为 agent RL 训练的稠密 reward 原料。
- **HTTP request-level session**（更细粒度）：声明了 HTTP 服务的节点可注入中间件，每个 HTTP 请求建一个 sub-cgroup。这样 webshell 触发的处理进程从请求到达那刻起就在正确的 session 里。这是 Phase 3 的可选增强，属于 Matcher 的一种特化。
- **Firecracker / libvirt substrates**：Phase 3 加 microVM 和 VM，sensor 通过 virtio-serial 把 guest 内事件回传 collector。`ObservationHook` 接口在 Phase 1 已经预留。
- **Replay Bundle**：`sysbox bundle create` 把 events.jsonl + match-report.json + field.hcl + state.json 打包，产出可字节级复现的 dataset artifact。

*预估工作量：2 engineers × 3 weeks = ~30 person-days，约 7 个 Task，每个 Task 1-2 天。*
