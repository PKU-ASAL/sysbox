# per-topology syscall 采集 —— cyberfield 侧对接说明

日期：2026-09-07

## 一、结论

sysbox **已经在每个容器上打了 Docker label**，其中 `sysbox.topology=<topology 名>`
是「按靶场隔离」的关键标识。因此：

- **不用**给 sysbox 加 cgroup 能力（Tetragon 也不支持 cgroup path scope，见
  `docs/design/` 里对应的架构复盘讨论）。
- 用 **一个 Tetragon 实例采集全部 syscall**，消费端按 `container_id` 反查 label、
  路由到「每靶场一个目录」。

## 二、sysbox 已打的 label（创建 node/network/router 时自动带上）

| label | 值示例 | 说明 |
|---|---|---|
| `sysbox.managed` | `true` | 标记「sysbox 管理的对象」，可用于粗过滤 |
| `sysbox.topology` | `cf-<campaign>-<team>-<gen>` | **按靶场隔离的关键标识，值就是 topology 名** |
| `sysbox.run_id` | `op-<hash>` | 本次 run |
| `sysbox.resource` | `sysbox_node.web` | 资源全地址 |
| `sysbox.resource_type` | `sysbox_node` | 资源类型 |
| `sysbox.resource_name` | `web` | 资源名 |

## 三、验证 label 是否打上

```bash
# 看某个容器的所有 label
docker inspect <container-id> -f '{{json .Config.Labels}}'

# 按 topology 过滤容器（列出某靶场的所有容器）
docker ps --filter "label=sysbox.topology=<topology-name>"

# 只看 topology 字段
docker inspect <container-id> -f '{{.Config.Labels.sysbox.topology}}'
```

## 四、Tetragon 侧：一个实例，采全部

1. 宿主机部署一个 Tetragon daemon（无需每靶场一个）。
2. TracingPolicy **不做 cgroup scope**（Tetragon 不支持 path scope），直接采全部
   （如需减量，先用 `matchBinaries` 等粗过滤，但别按 topology 过滤——那一步放消费端）。
3. 事件经 gRPC 流出，每条进程事件携带容器标识。**字段名要对照所用 Tetragon 版本
   确认**：通常非 K8s 的 Docker 环境是 `process.docker`（容器 ID），K8s 才是
   `process.pod`。

## 五、消费端：按 container_id 反查 label、落目录

流程：

```
收到 Tetragon 事件
  → 取 container_id（process.docker 字段）
  → docker inspect <container_id> → 读 sysbox.topology
  → 写入 /var/log/sysbox/<topology>/ 目录
```

伪代码：

```go
topology, err := dockerLabel(containerID, "sysbox.topology")
if err != nil {
    // 容器已销毁或非 sysbox 管理的，落 unknown/ 或丢弃
    return
}
appendEvent(filepath.Join(baseDir, topology, "events.jsonl"), event)
```

```bash
# label 反查一行命令（shell 版）
docker inspect <container-id> -f '{{.Config.Labels.sysbox.topology}}'
```

## 六、注意事项

1. **事件延迟 vs 容器销毁**：Tetragon 事件可能晚于容器销毁，此时 `docker inspect`
   会失败。建议：Tetragon 侧缓存「container_id → 首次关联时查到的 topology」，或
   消费端对查不到 label 的事件落到 `unknown/` 而不是丢弃。
2. **`sysbox.managed=true` 粗过滤**：不想让 Tetragon 采宿主机无关容器时，可先按
   `sysbox.managed=true` 筛（能否在 Tetragon selector 里用 label 匹配，需对照版本
   确认；不行就在消费端筛）。
3. **Tetragon 字段名**：`process.docker` / `process.container_id` / `process.pod`
   因版本而异，先抓一条事件打印确认。
