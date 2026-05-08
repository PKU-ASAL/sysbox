# sysbox Phase 2 — Observation & Session Anchor

> **Goal:** 让 sysbox 能观测在 field 里运行的**所有**进程，把进程活动绑定到入口 session（SSH 连接、webshell fork、node 自启动），输出带 `session_id` + `process_tree` 标注的事件 JSONL。Phase 2 结束时，能验证两个场景：① SSH 进去跑 nmap → 事件带 `session_id`；② 模拟 webshell（docker exec 注入 bash）→ 事件带 `process_tree: ["node-init","sh"]`，即使没有 SSH session 也能看到完整进程溯源链。

**Architecture:**
```
sysbox apply field.hcl
  └─ sensor subprocess (per node, Tracee)
       ├─ Process Tree Builder (pid→ancestry map，消费 fork/execve 事件)
       │     └─ 分层 Labeler
       │           ├─ cgroup_id → SSH session_id   (有 SSH 时)
       │           └─ pid → process_tree            (所有进程的溯源链，包括 webshell)
       ├─ cgroup v2 session enforcement (SSH ForceCommand)
       └─ session JSONL sink  →  /runs/<id>/events.jsonl
```

**Tech additions:**
- `github.com/aquasecurity/tracee/pkg/...` — syscall/event capture via eBPF（或调用 tracee binary）
- `github.com/opencontainers/runc/libcontainer/cgroups` — cgroup v2 操作
- 自制 `sysbox-sshd-hook` 二进制（ForceCommand wrapper）

**Out of scope (Phase 3+):** Prediction Matcher, Firecracker/libvirt, replay bundle.

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
│   │   ├── labeler.go         # 分层 Labeler: cgroup_id→session, pid→process_tree
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
│   │   ├── sensor_cmd.go      # sysbox sensor start/stop/status
│   │   └── session_cmd.go     # sysbox session list / attach
│   └── sysbox-sshd-hook/
│       └── main.go            # ForceCommand wrapper: creates session, writes cgroup
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
type Event struct {
    NodeID      string         `json:"node_id"`
    SessionID   string         `json:"session_id,omitempty"`  // set when SSH entry
    ProcessTree []string       `json:"process_tree,omitempty"` // ancestry: ["node-init","apache2","php-fpm","sh"]
    EntryPoint  string         `json:"entry_point,omitempty"`  // "ssh" | "webshell" | "node-init" | "exec"
    Timestamp   int64          `json:"ts"`       // unix nano
    PID         int            `json:"pid"`
    PPID        int            `json:"ppid"`
    Type        string         `json:"type"`     // "syscall" | "net" | "file"
    Name        string         `json:"name"`     // e.g. "execve"
    Args        map[string]any `json:"args"`
    IsAttack    bool           `json:"is_attack,omitempty"`
}

// EntryPoint 推断规则（由 Labeler 填充）：
//   - cgroup_id 命中 SSH session 表  → EntryPoint = "ssh",   SessionID = <id>
//   - ProcessTree 包含 "sysbox-exec"  → EntryPoint = "exec"
//   - ProcessTree[0] == "node-init" 且树深度 > 1 且最近父进程是 web server
//                                    → EntryPoint = "webshell"（Phase 3 Matcher 细化）
//   - 其他                           → EntryPoint = "node-init"

// Sensor observes a running node.
type Sensor interface {
    // Start begins observation. Events are written to the returned channel.
    Start(ctx context.Context, nodeID string, containerPID int) (<-chan Event, error)
    // Stop gracefully shuts down the sensor.
    Stop() error
}
```

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

### Step 3: Process Tree Builder（`pkg/sensor/proctree.go`）

Tracee 的 raw 事件里每条都带 `pid` / `ppid` / `comm`（进程名）。ProcessTreeBuilder 消费这些原始事件，在内存里维护一张 `pid → ProcInfo{comm, ppid}` 表：

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

// Ancestry 返回 pid 的祖先链（从 pid=1 的 node-init 到当前 pid），
// 以 comm 列表表示，例如 ["node-init","apache2","php-fpm","bash"]。
// 查不到的节点用 "?" 占位，避免因事件乱序导致链断。
func (b *ProcessTreeBuilder) Ancestry(pid int) []string
```

**重要约束：**
- 进程树表只在内存里，sensor 重启后从空开始（Phase 3 可持久化到 BoltDB）
- Tracee 启动时先接收已有进程的 `existing_process` 事件来预热进程表
- `Ancestry()` 最深追溯到 container 的 PID 1（即 `sleep infinity`，标记为 `node-init`）

**EntryPoint 推断**（在 `Ancestry()` 结果上做）：

| ProcessTree 特征 | EntryPoint |
|---|---|
| cgroup_id 命中 SSH session 表 | `"ssh"` |
| 树中有 `sshd` 但无 SSH session（异常） | `"ssh-orphan"` |
| 树中有 `sysbox-exec` | `"exec"` |
| 树根是 `node-init`，深度 ≥ 2 | `"node-init"` |

`"webshell"` 的判断依赖知道哪些进程是 web server，这是 Phase 3 Matcher 的工作，Phase 2 只输出原始 process_tree，不做推断。

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

### Step 3: 分层 Labeler

Labeler 持有两张表，按优先级分层查找：

```go
// Labeler 分层归因：先查 SSH cgroup 表，查不到则用进程树溯源。
type Labeler struct {
    mu          sync.RWMutex
    cgroupTable map[uint64]string          // cgroup_id → session_id（SSH 入口）
    tree        *sensor.ProcessTreeBuilder // pid → ancestry（所有进程）
}

// RegisterSSH 在 SSH session 建立时调用（由 sysbox-sshd-hook 通知）。
func (l *Labeler) RegisterSSH(cgroupID uint64, sessionID string)

// Annotate 填充 Event 的 SessionID / ProcessTree / EntryPoint 字段。
// 查找顺序：
//   1. cgroupTable[event.CgroupID] → sessionID（SSH）
//   2. tree.Ancestry(event.PID)   → processTree（所有进程）
//   3. EntryPoint 推断（见 Task 1 Step 3 规则表）
func (l *Labeler) Annotate(e *sensor.Event)
```

**node-init session：** apply 时，在容器启动后立刻读取容器 PID 1（`ContainerInspect.State.Pid`），把它写入 ProcessTreeBuilder 作为根节点（`comm = "node-init"`）。后续所有 fork 出来的进程都会自然挂在这棵树上。不需要单独的 node-init cgroup。

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

## Task 7: E2E tests — SSH session + webshell process tree

**Files:**
- Create: `tests/e2e/sensor_test.go`

两个独立测试函数，分别覆盖路径 A 和 SSH session 路径：

```
// TestSensorSSHSession（SSH 路径）:
// 1. apply hello-world field (with sysbox_ssh_access)
// 2. sysbox sensor start
// 3. SSH into node_a: run "nmap 127.0.0.1"
// 4. sleep 2s
// 5. parse runs/.../events.jsonl
// 6. Assert: execve(nmap) event has
//      session_id != ""
//      entry_point == "ssh"
//      process_tree contains "nmap"
// 7. destroy

// TestSensorWebshellTree（路径 A — 进程树溯源）:
// 1. apply hello-world field（无需 ssh_access）
// 2. sysbox sensor start
// 3. 模拟 webshell 入口：docker exec -it sysbox-node_a sh -c "wget -q google.com"
//    （docker exec 代替真实 webshell，测试进程树追踪）
// 4. sleep 2s
// 5. parse events.jsonl
// 6. Assert: execve(wget) event has
//      session_id == ""          // 不是 SSH，没有 session
//      entry_point == "exec"     // docker exec 入口
//      process_tree 包含 "sh" 和 "wget"
//      process_tree[0] == "node-init"
// 7. destroy
```

**说明：** 真实 webshell（通过 HTTP RCE）留给 Phase 3 的靶场场景测试；Phase 2 用 `docker exec` 作为"外部注入 shell"的等效代理，已经能验证进程树溯源逻辑。`docker exec` 会被标记为 `entry_point = "exec"`，而真正的 webshell（从 node-init 派生）会被标记为 `entry_point = "node-init"`——Phase 3 Matcher 再加规则区分。

---

## Phase 2 完成检查清单

**SSH session 路径：**
- [ ] `sysbox apply field.hcl` + `sysbox sensor start` 无报错
- [ ] SSH 进容器，`~/.profile` 里能看到 SYSBOX_SESSION_ID 环境变量
- [ ] 运行 nmap，在 events.jsonl 里能看到 `session_id != ""`, `entry_point = "ssh"`
- [ ] `sysbox session list` 显示活跃 session
- [ ] `sysbox sensor stop` 后事件停止写入

**进程树路径 A（webshell 溯源）：**
- [ ] `docker exec` 注入 sh，在 events.jsonl 里看到 `entry_point = "exec"` + `process_tree`
- [ ] process_tree 第一个元素是 `"node-init"`
- [ ] 容器里的 `sleep infinity`（PID 1）正确被识别为 `"node-init"` 根节点
- [ ] 进程树在 sensor 运行期间持续累积（不会因 exec 乱序而断链）

**通用：**
- [ ] 所有事件都有 `process_tree`，不管有没有 session_id
- [ ] E2E TestSensorSSHSession + TestSensorWebshellTree 均 PASS

---

## Phase 2 结束后，Phase 3 起步提示

- **Prediction Matcher**: 读 events.jsonl，结合 process_tree 特征匹配规则 → `is_attack` field
  - webshell 规则示例：`process_tree 中存在 [httpd|nginx|php-fpm] → [sh|bash|python]`
  - 横向移动规则：`process_tree 中存在 [sh] 且 execve = [nmap|masscan|nc]`
- **Replay Bundle**: `sysbox bundle create` 把 events.jsonl + field.hcl + state.json 打包
- **HTTP request-level session（路径 B）**：为声明了 HTTP 服务的 node 注入中间件，把每个 HTTP 请求映射为 sub-cgroup，让 webshell 事件也有精确的 `session_id`（而不只有 process_tree）
- **Firecracker substrate**: Phase 3 Task 1，需要 firecracker binary + KVM

*预估工作量：2 engineers × 3 weeks = ~30 person-days，约 7 个 Task，每个 Task 1-2 天。*
