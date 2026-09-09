# sysbox gc 子命令 —— 孤儿资源回收

日期：2026-09-09
状态：已评审（第一期 docker + libvirt，firecracker 第二期）

## 一、背景

SysField 等编排方在 rollout 被手动 Ctrl+C / SIGKILL 杀掉时，来不及调 sysbox 的
`destroy`，会留下三类孤儿：docker 容器、docker 桥接网络、libvirt VM。现有 `destroy`
只按单个 topology 的 state 清理，扫不到「state 之外」的孤儿对象。需要一个全局的
`gc` 子命令兜底。

分工：SysField 加 signal handler 处理可控中断（主）；sysbox 的 `gc` 兜底 SIGKILL 等
杀不掉的情况；文档记录手动清理（最后手段）。

## 二、孤儿识别（统一用 external ID 匹配）

`gc` 的核心是「收集 active 状态 → 扫外部对象 → 不在 active 集合里的就是孤儿」。

active 集合来自 `runsDir/*/state.json`：

| substrate | 外部对象来源 | 标识 | state 里的字段 |
|---|---|---|---|
| docker 容器 | `docker ps -a` + `sysbox.managed=true` label | container ID | `Resource.ContainerID()` |
| docker 网络 | `docker network ls` + `sysbox.managed=true` label | network ID | `Resource.DockerNetID()` |
| libvirt VM | `virsh list --all` + `sysbox-managed` title | domain name | `Resource.ExternalID` |

孤儿 = 「外部对象存在 + 带 sysbox 标识 + 其 ID 不在对应 active 集合里」。

## 三、CLI 设计

```
sysbox gc [--runs DIR] [--dry-run]
```

- `--runs`：state 根目录（默认 `config.DefaultRunsDir()`，即 `~/.sysbox/runs`）。
- `--dry-run`：只打印孤儿，不删除（默认 false）。

输出：每个孤儿一行 `type  id  (topology)`；非 dry-run 时执行回收并打印结果。

## 四、回收方式

- docker 容器：`docker rm -f <id>`
- docker 网络：`docker network rm <id>`
- libvirt VM：`virsh destroy <name>` + `virsh undefine <name>`

docker 网络要在容器之后删（容器还附着时网络删不掉）。

## 五、分期

**第一期（本设计实现）**：docker 容器 + docker 网络 + libvirt VM。

**第二期（后续）**：firecracker microVM。它不是 daemon 管理的对象，孤儿要靠
「进程扫描 + vm_dir/tap/socket 扫描」识别，需要额外的进程/设备扫描逻辑，单独做。

## 六、安全边界

- 只用 `sysbox.managed` label / `sysbox-managed` title 识别 sysbox 对象，绝不碰无
  标识的外部对象（避免误删别的实验）。
- 默认不删 state 文件（state 删除属于更激进的操作，第二期再评估）。
- `--dry-run` 是默认推荐先用的一步。
