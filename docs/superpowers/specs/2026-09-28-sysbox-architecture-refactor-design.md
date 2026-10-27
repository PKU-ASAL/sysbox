# Sysbox 整体架构重构设计

> 状态：历史设计稿，已被[观察驱动的整体重构方案](../../design/2026-09-28-observation-driven-runtime-refactor.md)取代。
> 本文保留方案 B 的讨论背景；生命周期、事务、执行 SPI 和重建边界以新方案为准。
>
> 日期：2026-09-28

## 1. 目标

本次重构解决四类问题：

1. 修复当前已确认的并发和一致性缺陷：Firecracker 进程状态竞争、workspace 非原子发布、本地持久化错误被静默忽略。
2. 统一 local filesystem、SQLite、Postgres 三套 API store 的编码、列表、版本和 CAS 语义。
3. 让 Firecracker 从“依赖当前 CLI 进程内的 `vmStore`”变成可跨进程发现、校验和接管的持久化运行时。
4. 保持 substrate 接入协议稳定：新 substrate 仍通过 `driver.Descriptor` 和 capability SPI 接入 sysbox。

允许破坏现有本地 state、SQLite/Postgres 数据库和 Firecracker 工作目录的兼容性。旧格式不做隐式迁移，启动时明确提示重建或清理。

## 2. 当前结构和问题

### 2.1 当前调用关系

```text
CLI / API
   ↓
runtime resource handlers
   ↓
driver.Registry / Descriptor
   ├── Node
   ├── NIC
   ├── NodeState
   ├── GuestExec
   ├── Console / Reset / Power / Network ...
   ↓
Docker / Firecracker / libvirt provider
```

这个 substrate SPI 仍然有效，问题主要在实现边界：

- API store 在 local、SQLite、Postgres 中重复实现相同的 JSON 转换、列表、CAS 和错误处理。
- `driver.GuestExec` 与 `substrate.Connection` 都表达节点内执行，语义部分重叠。
- Firecracker 的持久化 `HandleState`、进程锚点和 metadata 已存在，但事实来源仍是当前进程的 `vmStore`。
- workspace 更新采用“先删后写”，中途失败会暴露半成品目录。
- 本地对象读取失败会被跳过，数据损坏会伪装成对象不存在。

### 2.2 需要保持的稳定契约

每个 substrate 继续实现以下主契约：

| 契约 | 作用 |
|---|---|
| `driver.Node` | 创建、启动、停止、销毁、观察、恢复/adopt、建立连接 |
| `driver.NIC` | attach、observe、delete 网卡 |
| `driver.NodeState` | provider state 的序列化和反序列化 |
| `substrate.Connection` | 节点内执行、后台执行、文件复制、就绪等待 |
| 可选 capability | Console、Reset、Power、GuestNetwork、GuestFiles 等 |
| `driver.Descriptor` | 注册 provider 和其能力集合 |

runtime 不直接依赖 Docker API、Firecracker API 或 libvirt 命令。

## 3. 目标架构

### 3.1 总体分层

```text
                    ┌────────────────────────────┐
                    │ CLI / HTTP API / Scheduler  │
                    └──────────────┬─────────────┘
                                   │
                    ┌──────────────▼─────────────┐
                    │ Runtime / Domain Services  │
                    │ plan/apply/observe/recover │
                    └───────┬───────────┬────────┘
                            │           │
              ┌─────────────▼───┐   ┌──▼────────────────┐
              │ Stable Driver SPI│   │ Domain Repositories│
              │ Node/NIC/Conn/...│   │ Run/Agent/Guest/...│
              └─────────────┬───┘   └──┬────────────────┘
                            │          │
              ┌─────────────▼───┐   ┌──▼─────────────────┐
              │ Substrate        │   │ Shared codec/CAS   │
              │ docker/firecracker│   │ errors/versioning  │
              │ libvirt           │   └──┬─────────────────┘
              └───────────────────┘      │
                              ┌───────────▼──────────────┐
                              │ Backend adapters           │
                              │ filesystem / SQLite / PG   │
                              └────────────────────────────┘
```

核心原则：

- runtime 依赖稳定的领域接口，不依赖具体后端。
- repository 负责领域语义，backend adapter 只负责存储机制。
- provider 的 manifest、进程探测和恢复逻辑留在 provider 内部。
- optional capability 继续通过 registry 声明，不把所有 substrate 强行塞进一个大接口。

## 4. API store 重构

### 4.1 目标目录

```text
pkg/api/store/
├── model.go       领域存储模型和公共错误
├── codec.go       JSON 编解码、版本校验、敏感字段处理
├── cas.go         Compare-and-swap、lease、冲突语义
├── repository.go  面向业务的 repository 接口
├── filesystem/    本地目录 backend
├── sqlite/        SQLite backend
└── postgres/      Postgres backend
```

现有 `apiStore` 的领域接口继续保留或逐步迁移到 `store.Repository`，但不再让每个 backend 重新定义对象转换规则。

### 4.2 共享层职责

共享层统一以下行为：

- 对 `Run`、`Agent`、`GuestExecution`、`GuestFileOperation` 等对象进行编码和解码；
- 统一 `NotFound`、`Conflict`、`InvalidVersion`、`CorruptRecord` 等错误；
- 统一列表排序、空结果和损坏记录处理；
- 统一 CAS 规则：读取版本 `v`，只有存储中的版本仍为 `v` 时才写入 `v+1`；
- 统一 lease 的 owner、TTL、过期和续租语义；
- 对敏感字段执行统一 redaction，避免 backend 实现自行决定是否落盘。

backend 只负责：

- 读取/写入原始记录；
- 在 SQLite/Postgres 中提供事务和条件更新；
- 在 filesystem backend 中提供临时文件、rename 和文件锁。

### 4.3 推荐的领域接口

```go
type Repository[T any] interface {
    Get(ctx context.Context, id string) (T, error)
    Put(ctx context.Context, value T) error
    List(ctx context.Context, scope Scope) ([]T, error)
    UpdateCAS(ctx context.Context, id string, expected int64, fn func(*T) error) (bool, error)
}

type LeaseRepository[T any] interface {
    Claim(ctx context.Context, id, owner string, ttl time.Duration) (T, bool, error)
    Renew(ctx context.Context, id, owner string, ttl time.Duration) (T, bool, error)
}
```

实际实现可以按对象类型提供更具体的方法，但 CAS、错误和 codec 必须来自共享层，不能在三套 backend 中复制。

### 4.4 破坏兼容策略

- 提升 API schema generation；旧 SQLite/Postgres schema 启动时返回“不兼容，需要重建”。
- local filesystem 使用新的目录布局和记录版本；旧 JSON 文件不自动兼容。
- 提供显式的 `sysbox reset-state` 或等价清理命令，删除本地 API store 和 topology state。
- 不提供后台隐式迁移，避免半迁移状态。

### 4.5 必须有的契约测试

同一组测试套件运行在 filesystem、SQLite、Postgres：

- 保存、读取、列表排序；
- 不存在对象的统一错误；
- 损坏记录返回 `CorruptRecord`，不能静默跳过；
- 两个并发 CAS 只有一个成功；
- lease claim/renew/过期；
- context cancel 能中止长查询；
- 敏感字段不会以明文进入公共 payload 或日志。

## 5. 节点内执行 SPI

### 5.1 保留的主入口

`substrate.Connection` 作为节点内执行的主 SPI：

```text
driver.Node.Connection(handle, hints)
             ↓
substrate.Connection
             ├── Exec
             ├── Start / background execution
             ├── Copy
             └── WaitReady
```

Docker、Firecracker、libvirt 分别返回自己的 connection 实现。`driver.GuestExec` 暂时保留以维持现有代码边界；没有独特语义的实现逐步转发到 `Connection`，避免两套行为继续分叉。

### 5.2 执行结果和后台任务

当前 `ExecBackground` 返回 `int`，不能表达 Docker exec ID、SSH 远程 PID、vsock guest PID 和 microVM opaque PID 的差异。重构后引入：

```go
type ExecutionHandle struct {
    ID         string
    Provider   string
    PID        int
    PIDMode    PIDMode
    StartedAt  time.Time
}
```

同时扩展 `ExecResult` 的状态语义，至少区分：正常退出、非零退出、超时、取消、输出截断和连接失败。

交互式终端继续使用独立的 `ConsoleProvider`，不与普通 exec 合并。

## 6. Firecracker 可恢复架构

### 6.1 事实来源

`vmStore` 降级为进程内缓存，不能再作为 VM 是否存在的依据。每个 VM 目录包含一个 manifest：

```text
<run-root>/<vm-id>/
├── manifest.json
├── vm_config.json
├── firecracker.pid
├── sysbox.json
├── firecracker.sock
├── rootfs.ext4
└── config.ext4
```

manifest 至少包含：

```json
{
  "format": 2,
  "vm_id": "...",
  "vm_dir": "...",
  "config_path": "...",
  "config_sha256": "...",
  "socket": "...",
  "pid_file": "...",
  "pid": 1234,
  "pid_start": "...",
  "netns": "...",
  "tap_devices": ["..."],
  "vsock_uds": "...",
  "vsock_cid": 7,
  "status": "running",
  "ready": true,
  "updated_at": "..."
}
```

### 6.2 Runtime registry

新增 provider 内部的 `RuntimeRegistry`，职责是：

- 从 manifest 加载 VM；
- 校验 VM 目录是否属于 sysbox、配置摘要是否匹配；
- 通过 PID + start time + socket 验证进程归属，防止 PID 复用；
- 通过 Firecracker socket/vsock 探测实际状态；
- 将运行中的 VM adopt 到当前进程；
- 将 exited、missing、drifted、unknown 转换为统一 `NodeObservation`；
- 在生命周期操作结束后原子更新 manifest。

```text
CLI/API 启动
    ↓
扫描 manifest
    ↓
验证目录、配置、PID start time、socket
    ├── running  → adopt 到 RuntimeRegistry
    ├── exited   → 记录退出信息
    ├── missing  → 返回明确缺失状态
    └── drifted  → 禁止破坏性操作并要求修复
```

### 6.3 生命周期规则

- `CreateNode` 创建 manifest，状态为 `prepared`；
- `Attach` 更新 NIC、namespace 和配置摘要；
- `StartNode` 使用 manifest 中的完整启动信息，成功后原子写入 `running`；
- `ObserveNode` 优先查询真实进程和 socket，再返回 manifest 状态；
- `AdoptNode` 不重新创建 Firecracker，只验证并登记现有进程；
- `StopNode`、`DestroyNode` 必须校验 process anchor，不能只按 PID 杀进程；
- `vmStore` 只保存 `*exec.Cmd` 等当前进程对象，进程重启后重新从 manifest 建立缓存；
- manifest 更新使用临时文件 + rename，避免控制进程崩溃留下半份记录。

### 6.4 重建策略

旧 VM 目录不自动迁移。manifest 缺失或 `format` 不匹配时：

- `observe` 返回 `unknown/drifted` 诊断；
- `apply` 不复用旧目录；
- `destroy` 只有在明确验证目录归属后才允许清理；
- 提供清理命令删除旧 Firecracker 工作目录。

## 7. 立即修复和实施顺序

### 阶段 1：低风险一致性修复

1. 为 Firecracker 的 `vmStore` 和 `vmProcess` 建立明确锁规则，修复 `started/cmd/pid` 的 race。
2. 为 workspace 更新增加 staging 目录和原子 rename。
3. 让本地对象读取显式返回损坏、权限和 glob 错误。
4. 增加 race、并发 CAS、workspace 中途失败和损坏记录测试。

### 阶段 2：API store 收敛

1. 建立共享 codec、错误类型和 CAS helper。
2. 先迁移 `GuestExecution`、`GuestFileOperation` 和 `Run`，这三类对象已经有明确版本/CAS 语义。
3. 将 filesystem、SQLite、Postgres 接到同一套 contract tests。
4. 迁移 Agent、policy、checkpoint、health 等对象。
5. 提升 schema generation，旧数据库明确拒绝启动并提示重建。

### 阶段 3：Firecracker manifest 和跨进程恢复

1. 定义 manifest schema 和原子读写器。
2. 把现有 PID anchor、metadata、HandleState 的字段收敛到 manifest。
3. 实现 process probe、socket probe 和 `RuntimeRegistry.Adopt`。
4. 修改 `ObserveNode`、`NodeStatus`、`StopNode`、`DestroyNode` 走 registry。
5. 增加“上一 CLI 退出后，新 CLI 发现并 adopt 正在运行 VM”的集成测试。

### 阶段 4：执行 SPI 收敛

1. 增加 `ExecutionHandle` 和统一执行状态。
2. 让 Docker、Firecracker、libvirt 的 `Connection` 实现统一超时、取消、输出上限和错误语义。
3. 将没有独特行为的 `GuestExec` 实现转发到 `Connection`。
4. 保留 Console 为独立 capability。

## 8. 验收标准

### 并发和一致性

- `go test -race ./...` 通过；
- 两个并发 Firecracker lifecycle 调用不会重复启动；
- workspace 发布过程中进程退出，读取方只能看到旧版本或完整新版本；
- 损坏 local record 会返回明确错误，不会被当成不存在。

### 存储后端

- filesystem、SQLite、Postgres 通过同一套 contract tests；
- CAS 冲突、NotFound、排序和 lease 行为一致；
- 旧 schema 被拒绝，清理后可创建全新 schema。

### Firecracker 恢复

- 新 CLI 能扫描 manifest 并 adopt 仍运行的 VM；
- PID 复用、socket 指向错误进程、配置摘要不匹配都会被拒绝；
- `ObserveNode` 能区分 running、exited、missing 和 drifted；
- `DestroyNode` 不会因为旧 PID 文件误杀其他进程。

### Substrate 扩展

- 新 substrate 只需实现稳定 SPI 和 capability contract tests；
- runtime 不出现按 provider 名称分支的执行逻辑；
- Docker、Firecracker、libvirt 的节点内执行均通过统一 Connection 语义验证。

## 9. 明确不做的事情

- 不在本轮引入独立 Firecracker supervisor daemon；
- 不保留旧 state/database/VM directory 的隐式兼容路径；
- 不把所有 optional capability 合并成一个巨型 substrate 接口；
- 不让 API store backend 直接暴露给 runtime；
- 不把交互式 Console 强行建模成普通 Exec。

这套设计保留 sysbox 当前最有价值的扩展点：稳定的 substrate capability SPI；同时把存储一致性和 provider 运行时恢复从具体实现细节提升为清晰、可测试的内部架构。
