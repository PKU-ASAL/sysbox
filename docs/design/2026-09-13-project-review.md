# Sysbox 项目审查记录

审查日期：2026-09-13。基于 commit `e359226`。以下问题均待修复；本次只记录审查结果。

## P1：GC 使用不完整 state 清单判断整个宿主机的孤儿资源

- 位置：`cmd/sysbox/commands/gc_cmd.go:85`。
- `collectActiveIDs` 只扫描指定目录的 `*/state.json`，忽略目录扫描和 state 读取错误；`findOrphans` 却检查整个宿主机所有带 Sysbox 管理标记的资源。
- 触发：state 损坏、扫描目录错误、资源使用其他 state backend 或存放在其他工作区。
- 影响：正常资源可能被当作孤儿强制删除。
- 验证：临时 Go overlay 测试确认损坏 state 会让原本受保护的 container ID 消失，不存在的目录也被接受为空清单。未执行真实资源删除。
- 修复方向：清单不完整时拒绝清理；识别实际 backend，并以可验证的 workspace/topology 所有权限制范围。

## P1：apply 的 revision 未绑定不可变执行目录

- 位置：`pkg/api/run_service.go:132`、`pkg/agentexec/executor.go:299`。
- API 每次 apply 重写 topology 共享目录，Agent 执行时读取该目录，不按 `Run.Revision` 固定文件树。API 请求锁不覆盖独立 Agent 的执行过程。
- 触发：revision A 的 run 尚未执行，又向同一 topology 提交 revision B。
- 影响：A 的 run 可能执行 B 的配置，run 记录与实际执行内容不一致。
- 验证：通过模拟 HTTP 请求确认 A 仍处于 assigned 时，共享 HCL 已变为 B。
- 修复方向：每个 run 使用不可变 revision 文件树；串行化 topology 的生命周期操作。

## P1：后续生命周期操作未固定 topology 所属 Agent

- 位置：`pkg/api/scheduler_service.go:28`。
- 调度根据请求指定的 Agent 或 capability 选择机器，没有约束为 topology 已有资源所在宿主机。
- 触发：首次指定 host-b，后续 apply 未指定 Agent，host-a 同样满足 capability。
- 影响：后续任务可能在其他宿主机重复创建资源，或无法操作原宿主机上的资源。
- 验证：模拟请求确认同一 topology 的两次 apply 分别被分配至 host-b 和 host-a。未执行真实跨宿主机任务。
- 修复方向：持久化 topology 所属 Agent；后续操作遵守绑定，迁移作为独立显式流程处理。

## P1：state 读取失败仍允许普通元数据删除

- 位置：`pkg/api/workspace_service.go:182`。
- `Delete` 只在读取 state 成功且存在资源时检查 force；读取错误会继续删除 state 目录及 workspace。
- 触发：state JSON 损坏后，不带 `force=true` 调用 DELETE topology。
- 影响：无法确认资源状态时仍丢失恢复数据，外部资源可能失去追踪。
- 验证：临时目录中损坏 JSON 后，模拟 DELETE 返回成功且 state 文件消失。
- 修复方向：读取失败时返回错误，保留恢复材料。

## 验证范围

- `go test ./...`：通过。
- `go vet ./...`：通过。
- `go test -race ./pkg/api ./pkg/agentexec ./pkg/state ./pkg/runtime`：通过。
- 最小复现使用临时目录和模拟请求，没有操作真实 Docker、Firecracker 或 libvirt 资源。
- 未运行真实异构环境验收。
