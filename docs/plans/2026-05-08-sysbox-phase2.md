# sysbox Phase 2 — Observation & Session Anchor

> **Goal:** 让 sysbox 能观测在 field 里运行的进程，把进程活动绑定到"谁通过 SSH 进来的哪条 session"，输出带 `session_id` 标注的事件 JSONL。Phase 2 结束时，能用一条命令起一个靶场、SSH 进去跑 nmap，然后在 host 上看到带 `is_attack=true` 标注的事件流。

**Architecture:**
```
sysbox apply field.hcl
  └─ sensor subprocess (per node, Tracee)
       └─ cgroup v2 session enforcement
            └─ sshd ForceCommand wrapper
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
│   │   └── sensor_test.go
│   ├── session/
│   │   ├── session.go         # Session 数据结构 (ID, NodeID, User, StartTime, CgroupID)
│   │   ├── cgroup.go          # cgroup v2: 新建 session cgroup, 迁移 PID, 读 cgroup_id
│   │   ├── labeler.go         # 事件 → session 归属 (cgroup_id lookup table)
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
    NodeID    string            `json:"node_id"`
    SessionID string            `json:"session_id,omitempty"`
    Timestamp int64             `json:"ts"`       // unix nano
    Type      string            `json:"type"`     // "syscall" | "net" | "file"
    Name      string            `json:"name"`     // e.g. "execve"
    Args      map[string]any    `json:"args"`
    IsAttack  bool              `json:"is_attack,omitempty"`
}

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

### Step 3: 集成到 executor.createNode

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

### Step 3: Labeler

```go
// Labeler maps cgroup_id -> SessionID so the sensor can annotate events.
type Labeler struct {
    mu   sync.RWMutex
    table map[uint64]string
}
func (l *Labeler) Register(cgroupID uint64, sessionID string)
func (l *Labeler) Lookup(cgroupID uint64) string
```

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

## Task 7: E2E test — SSH + sensor

**Files:**
- Create: `tests/e2e/sensor_test.go`

```
// TestSensorSession:
// 1. apply two-networks field
// 2. sysbox sensor start
// 3. SSH into node_a as attacker: run "nmap 10.0.2.0/24"
// 4. Wait 2s
// 5. cat runs/.../events.jsonl | jq 'select(.name=="execve" and .args.pathname=="/usr/bin/nmap")'
// 6. Assert event has session_id != "" and is_attack candidate fields
// 7. destroy
```

---

## Phase 2 完成检查清单

- [ ] `sysbox apply field.hcl` + `sysbox sensor start` 无报错
- [ ] SSH 进容器，`~/.profile` 里能看到 SYSBOX_SESSION_ID 环境变量
- [ ] 运行 nmap，在 events.jsonl 里能看到对应 execve 事件带 session_id
- [ ] `sysbox session list` 显示活跃 session
- [ ] `sysbox sensor stop` 后事件停止写入
- [ ] E2E test PASS（不需要 sudo 以外的特殊权限）

---

## Phase 2 结束后，Phase 3 起步提示

- Prediction Matcher: 读 events.jsonl，用规则匹配 → `is_attack` field
- Replay Bundle: `sysbox bundle create` 把 events.jsonl + field.hcl + state.json 打包
- Firecracker substrate: Phase 3 Task 1，需要 firecracker binary + KVM

*预估工作量：2 engineers × 3 weeks = ~30 person-days，约 7 个 Task，每个 Task 1-2 天。*
