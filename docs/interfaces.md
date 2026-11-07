# Sysbox 接口文档

本文是当前 CLI、HCL、HTTP、宿主/guest 协议、provider SPI 和存储兼容边界的唯一接口文档。
[架构设计](architecture.md)说明当前职责与限制，[快速开始](../README.md)提供构建、部署和使用步骤。
本文保留字段、路由与恢复约束；版本差异以对应提交的源码和 binary help 为准。

- [HCL](#hcl)
- [CLI](#cli)
- [HTTP API](#http-api)
- [Host supervisor / guest 协议](#host-protocol)
- [存储与恢复](#recovery-contract)
- [Provider SPI](#provider-spi)

<a id="host-protocol"></a>
## Host supervisor 与 guest 协议

`sysbox agent start` 只支持独立 host-supervisor，没有本机执行 fallback。
启动必须是非 root、无 permitted/effective/ambient capability、`NoNewPrivileges=1`，并显式指定 worker 专用 `--config`。
配置 `agent.host_supervisor_socket`、`agent.execution_grant_public_key_file`、`agent.host_public_key_file` 和持久 execution session。
配置文件、公钥和 socket 路径必须 root 拥有，祖先目录不能由 worker 改写。worker 配置不能含管理员 API token、数据库或私钥路径。
注册时声明的 capabilities/labels 不接受 heartbeat 或重连改写；修改能力声明需由管理员重新注册身份。
API 持有 issuer 私钥，supervisor 持有独立 host 私钥；API 的 host 公钥映射按 Agent ID 固定。
Ed25519 原始私钥 64 字节、公钥 32 字节；不要把 host 私钥交给 worker。

```yaml
paths:
  runs_dir: /var/lib/sysbox-host/runs
agent:
  execution_grant_public_key_file: /etc/sysbox-worker/issuer.pub
host_supervisor:
  socket: /run/sysbox-host/host.sock
  root: /var/lib/sysbox-host/tasks
  signing_key_file: /etc/sysbox-host/host.key
  worker_group_id: 32000
```

上面仅展示 supervisor 相关字段，部署还需配置 API 地址、Agent identity 和对应 repository。
使用 `sysbox host-supervisor --config /etc/sysbox-host/sysbox.yaml --identity /etc/sysbox-host/identity.json` 启动。
私有配置、key 和 canonical repository 必须位于可信绝对路径，不能经可写祖先或 symlink 替换；
socket 目录 root 拥有，worker group 只能连接 socket，不能修改目录。

Agent 持自己的 HMAC 通信凭据；API token 启用后，只有明确列出的本身份运行协议可用 HMAC 认证，管理接口仍需管理员凭据。
宿主 `host-observe` RPC 自己分配采集序号，签名包含 Agent、session、boot、采集时间、能力/容量和资源健康。
Agent 通过 `POST /v1/agents/{agent}/observations` 原样转发，API 验签、核对当前 session，原子保存整批数据。
快照超过 1 分钟拒绝接收，已存的资源健康过期后显示 unknown；Agent 心跳不能延长宿主观察的有效期。
旧的 unsigned inventory/resource-projection 写接口、worker Run/node/file complete 接口与 worker adoption-confirm 接口已删除。
暂停和恢复也由宿主签名回执完成：只有 provider 返回成功且实际状态匹配才签名，未知结果保留待确认。

宿主 Unix socket 使用版本化 JSON Request/Response；操作及字段定义见
[protocol.go](../pkg/hostsupervisor/protocol.go)、[server.go](../pkg/hostsupervisor/server.go)、
[Run 协议](../pkg/hostsupervisor/run_protocol.go)。worker 提交 canonical TargetRef 与签名授权，
不能选择 provider endpoint、宿主文件路径或数据库位置。

服务停止时先停止接收请求，再取消并等待在途处理结束，最后关闭执行存储。
Agent 等待命令、观测和续租；supervisor 等待已受理的 Run、授权检查、回执和本地 guest 观察器。
客户端断开不终止宿主已受理的 Run；服务退出时的取消也不能代替远端停止证明，未确认的结果留待恢复。

| 消息/记录 | 必须绑定 | 语义 |
|---|---|---|
| ExecutionGrant | Agent、session、scope UID/generation、command/action、attempt、epoch、digest、boot、期限 | 有效期内允许指定效果；不等于效果完成 |
| RunRequest | Run incarnation、revision、target、expected serial、操作 | host 再读 canonical state/Run 校验 |
| Task Request | ID、argv/env/input 摘要、执行身份、输出配额、grant | 相同 ID 和摘要去重，变化拒绝 |
| Cancellation | 原 grant 身份与 stop-only 签名 | 不允许重新 Start 或 Renew |
| Host receipt | 原执行身份、宿主签名、停止/结果证据 | API 验证并幂等提交；worker event 只作投递进度 |
| Observation | 身份、代际、来源、时序及必要签名 | 当前事实；不能单独证明旧请求停止 |

### Linux tasks 与文件

SSH 调用预装 `/usr/local/bin/sysbox-guest-task --guest-task`；vsock 使用 `task.v1`。
docker 节点不经过 guest helper：后台执行由 Docker 守护进程的 detached exec 承担，观察和停止都以守护进程的
`ContainerExecInspect` 为准，镜像无需预装任何文件。因此依赖 guest helper 的能力（输出游标、租约续期、
带范围的 durable file install）只适用于 OS guest；docker 节点对 background provisioner 的支持不依赖这些能力。
OS guest 的 helper 固定 issuer 公钥路径 `/etc/sysbox/grant-public-key`，无 unsigned/nohup fallback。
普通持久任务默认 UID/GID 65534、清除附加组/capabilities、no_new_privs；同 UID 任务不提供相互可用性隔离。
需要管理员委派的 cgroup v2 根 `/sys/fs/cgroup/sysbox-tasks` 和私有 `/var/lib/sysbox/guest-tasks`。
任务 request argv/env/stdin 不作为普通任务记录落盘；输出持久化且可能含敏感数据。

Start/Observe/Cancel/Renew/ReadOutput 共享 task ID、digest、boot、scope 和 epoch。
读输出使用 cursor，单页至多 64 KiB；取消需观察到整棵任务树停止，guest reboot 只能证明旧进程停止，不能补造 exit code。
watchdog 使用与 boot 绑定的单调时间预算；连接断开或 API lease 过期不等于 stop receipt。

固定文件安装签名绑定 ID、canonical guest path、mode、size、SHA-256；payload 上限 16 MiB。
管理员预建目录并配置 root-owned `/etc/sysbox/install-roots.json`；不扩展根目录白名单。
同文件系统 staging → commit intent → rename/fsync → durable completion；丢回复后观察原记录，不盲目重新 rename。
Vsock `ready.v1` 是初始化门槛，ping 仅表示连通；旧 `exec.v2` / `write_file.v2` 传输不代表所有 foreground 调用已有持久任务保证。

### Windows QGA 与 Firecracker policy

Windows 使用 `C:\ProgramData\Sysbox\guest-task\sysbox-windows-task.exe`。
构建：`scripts/windows/task/build.sh OUTPUT`；逐节点安装：[install.ps1](../scripts/windows/task/install.ps1)。
配置绑定 issuer、topology/resource UID 与 generation；共享镜像不能含节点身份或 worker 凭据。
普通任务使用非管理员账户，原子加入 Job Object；helper/journal/凭据受 SYSTEM/Administrators ACL 保护。
QGA 请求 envelope 上限 32 KiB（含 base64 和签名元数据）。不支持大输入分块、持久文件发布或特权 ETW 的任意命令回退。
已记录真实验收平台为 Windows Server 2025 amd64，不能推及所有 Windows 版本。

Firecracker Policy 使用签名固定 built-in `sysbox-policy.v1`，普通任务仍不得任意 root 执行。
绑定 node/policy 身份、digest 与授权，guest journal 保留 generation 和 deletion tombstone；
内核需支持 nftables/netfilter，并准备匹配的 nft userspace。guest policy 与 VMM jailer 是独立的隔离层。

<a id="artifacts"></a>
## 制品与可移植性

`sysbox_image` / `sysbox_kernel` 明确 kind、architecture、guest_family 与不可变 digest。
Docker 长期基线固定 OCI digest；daemon-local image ID 用于创建/观察，RepoDigest 锁用于可移植身份，不能混为同一个值。
VM kernel 使用匹配架构、virtio/vsock 支持的 vmlinux；Firecracker rootfs 使用 ext4，libvirt 使用只读 qcow2/cloud-image 基线和每代独立 overlay。

[prepare-fc-rootfs.sh](../scripts/prepare-fc-rootfs.sh)准备 rootfs；[microvm 示例](../examples/microvm)和
[异构示例](../examples/heterogeneous-matrix)给出配置。guest helper、issuer 公钥和初始化 readiness 必须匹配。
构建 rootfs 本身不保证 nft policy 内核功能或 VMM sandbox 已具备。

生命周期 CLI 现在提交远程 Run；制品必须通过不可变 revision 相对路径或固定 SHA-256 的 HTTP(S) 来源交付，
不能假设 API 的绝对路径在 Agent 可见。大 rootfs/kernel/qcow2 不放进有大小限制的 revision JSON。

VM `source` 可为本地路径或 URL；`pull_source` 提供同字节的远端后备，必须固定 SHA-256。
本地文件存在但摘要不符时拒绝，不能通过下载掩盖漂移；本地不存在才检查缓存或下载。
缓存校验后原子发布；Reset 在破坏性动作前重新检查基线。OCI provider 不使用 VM 的 pull_source 规则。
下载来源必须直接提供所声明的原始字节；该接口不负责创建或发布镜像。

<a id="recovery-contract"></a>
## 存储、升级与恢复契约

| 数据层 | 当前格式 / 来源 | 升级规则 |
|---|---|---|
| API SQLite / PostgreSQL | schema 15，[store.go](../pkg/controlstore/store.go) | 空库初始化/当前版本重开；旧 API 表不自动迁移 |
| Lifecycle aggregate | format 16 | 只接受当前格式；旧数据归档并重建，不在线导入 |
| Execution authority | 基线 5；GC disposition 6、adoption disposition 7、intent disposition 8 | [authority_validation.go](../pkg/persistence/authority_validation.go)校验；不能仅看基线常量 |
| Guest task | record 2；Linux RPC 1、scope 2/3 | 旧 helper 不支持的新 scope 必须拒绝，不能修改 version 绕过 |
| Host task | 独立宿主任务记录版本 | 以 [task.go](../pkg/hostsupervisor/task.go) 校验为准，不与 guest record 混用 |
| Firecracker / 网络 ownership | manifest v3、网络 anchor v2 与 attachment target identity | 缺 ownership 不能自动猜测或补齐 |

版本各自属于不同故障域，不能合并成一个总数字。生命周期已统一为 format 16；执行授权记录仍有自己的版本校验，不能手改字段绕过。

旧 lifecycle 格式直接拒绝；升级前须停止旧 writer、确认并清理旧资源，再归档原始数据并重新部署。
归档不解除 unknown、不证明外部资源 absent。先用原版本确认并清理旧资源，再部署新版本。
同步升级 API、Agent、supervisor、CLI 和 guest helper；无法迁移的旧现场使用原 binary/原证据进行独立处置。

恢复原则：固定 revision/Run incarnation → 读取原 journal → 验证停止/授权 → 观察精确身份 → 提交共享状态转换。
不得从 checkpoint 文本、resource absent、PID 数值或租约过期推断原效果成功/终止。
Reset continuation 绑定原阶段与返回证据；snapshot restore 只恢复元数据，不提供 VM 磁盘快照能力。

Fencing 的 recovery-plan / observe / reconcile-delete / reconcile-intents / release 为分步协议；
需要精确锚点、签名观察和对应处置前提。它们不提供任意“force 清零”通道。
原始历史 unknown intent 与冻结必须保留；新版本、新 key 或新 authority 目录不能绕过。

文件/SQLite 的 store scope 为 host-local；共享 PostgreSQL 可支持声明的 multi-API 能力。
API 的 [capabilities](../pkg/api/capabilities.go)不会无条件声称 external absence 或 resource revocation 已得到证明。
恢复敏感输入可能需要向实际接收恢复请求的 API 重交完全相同的请求；输入未补齐时拒绝执行，不能回退默认值。

<a id="provider-spi"></a>
## 内部 Provider 与 Repository 契约

[driver.Descriptor](../pkg/driver) 注册实现的能力；consumer 必须 Require 对应 capability，未实现返回 unsupported。
[driver/capability.go](../pkg/driver/capability.go)定义 Node、NIC、GuestExec、GuestFiles、Network、Policy 等接口。Console 复用受控 guest task，不再单列 provider 执行接口。
[substrate](../pkg/substrate)承载执行类型与身份；runtime 不解析 container name、VM socket 或 provider-private envelope。

| 能力 | 核心契约 |
|---|---|
| Node / NodeState | Create/Start/Stop/Destroy/Read + typed handle / 私有状态 codec；外部 ID 不等于逻辑 address |
| NIC / network | attachment name + network canonical identity + 双端代际/ownership；不得按同名 bridge 删除 |
| Policy | Apply/Observe/Delete/Discover；readback 与 semantic digest 验证；当前策略语义以 IPv4 为主 |
| Task / file | transport 传递固定协议；不能把 SSH/QGA 的连接成功当授权/初始化/任务完成 |
| Adoption / transfer | 先只读 plan，再精确 proof / reservation / canonical commit；不同 owner 不隐式抢占 |
| Quiescence | 必须证明同一 operation 停止；不支持时返回 unknown，不能用当前存在性替代 |
| Repository | atomic action/state、CAS、锁/租约、maintenance barrier；后端实现实际原子性，业务转换共享 |

当前资源核心入口为 `runtime.PrepareOperation(ctx, OperationRequest, OperationDependencies)`，
返回的 Operation 提供 Plan(ctx)/Execute(ctx, plan)；Dependencies 显式提供 Providers、Manager、Actions，可提供 Recorder、Log、Secrets。
调用方从准备到最终结果认证持有 topology execution lock。该入口不是新的 HTTP endpoint。
规划/健康/资源观察类型位于 `pkg/model`；workspace、planner、observer 和 executor 的 Go API 显式接收 provider registry，
不再通过 runtime 内部的全局 registry 选择 provider。HTTP JSON 字段和既有请求语义没有因此更名。

`hostexec.Open(cfg)` 创建宿主执行环境，初始化失败返回错误。它不启动 API 服务，也不持有 issuer 私钥。
Agent 只转发请求，不创建执行环境。Run 最终状态由 API 验证宿主完成回执后提交；关闭执行环境前必须等待所有使用者退出。

<a id="hcl"></a>
<a id="hcl-hcl-参考"></a>
## HCL 参考

本页列出 Sysbox 当前配置结构。示例和工作流见 [Authoring Topologies](architecture.md)。

<a id="hcl-顶层块"></a>
### 顶层块

| Block | Purpose |
|---|---|
| `substrate TYPE {}` | Register a provider implementation under an alias |
| `variable NAME {}` | Declare input and optional default |
| `locals {}` | Define local expressions |
| `module NAME {}` | Load a child configuration from `source` |
| `data TYPE NAME {}` | Read external data through a data source |
| `resource TYPE NAME {}` | Declare managed topology intent |
| `output NAME {}` | Export an evaluated value |

<a id="hcl-表达式与引用"></a>
### 表达式与引用

Resource reference uses typed canonical identity:

```hcl
image   = sysbox_image.web.id
network = sysbox_network.dmz.id
```

Modules, `count` and `for_each` produce structural addresses. String instance keys remain JSON quoted. `env("NAME")` creates a secret reference; `env_optional("NAME")` is for non-sensitive optional lookup.

<a id="hcl-substrate"></a>
### Substrate

```hcl
substrate "docker" {
  alias = "local"
}
```

Required: type label and `alias`. Provider-specific substrate configuration remains in the block body.

<a id="hcl-通用生命周期"></a>
### 通用生命周期

Resources that support lifecycle accept:

| Field | Type | Meaning |
|---|---|---|
| `prevent_destroy` | bool | Reject delete or replacement planning |
| `ignore_changes` | list(string) | Ignore schema-supported desired paths |

Use ignore sparingly; it does not transfer ownership to another system.

<a id="hcl-sysbox_image"></a>
### `sysbox_image`

| Field | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Substrate reference |
| `kind` | yes | `oci`, `rootfs`, `qcow2`, or provider-supported kind |
| `source` | yes | Registry reference, URL or local path |
| `pull_source` | no | VM only: HTTP(S) fallback when a local `source` is absent; requires `sha256` |
| `architecture` | yes | Guest architecture such as `amd64` |
| `guest_family` | yes | Guest OS family, currently Linux contracts |
| `sha256` | no | Immutable content digest |
| `size` | no | Expected/generated artifact size |

<a id="hcl-sysbox_kernel"></a>
### `sysbox_kernel`

| Field | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Firecracker-capable substrate |
| `architecture` | yes | Kernel architecture |
| `source` | yes | Kernel source/path |
| `pull_source` | no | HTTP(S) fallback when the local kernel file is absent; requires `sha256` |
| `sha256` | no | Immutable digest |
| `cmdline_template` | no | Provider kernel command-line template |
| `depends_on` | no | Additional resource addresses |

<a id="hcl-sysbox_network"></a>
### `sysbox_network`

| Field | Required | Default | Meaning |
|---|---:|---|---|
| `cidr` | yes | | IPv4 network prefix |
| `type` | no | provider default | Network implementation type |
| `nat` | no | `false` | Managed outbound NAT intent |
| `root_address` | no | | root bridge 上用于入口路由/NAT 的 IPv4 地址；为空时不暴露宿主机 L3 路由 |
| `root_route` | no | | 通过 root bridge 代理的宿主机路由；包含 `dst` 和 `via` |
| `lifecycle` | no | | Lifecycle block |

<a id="hcl-sysbox_node"></a>
### `sysbox_node`

<a id="hcl-通用字段"></a>
#### 通用字段

| Field/block | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Node provider reference |
| `image` | yes | `sysbox_image` reference |
| `guest_family` | no | Override/validate guest family |
| `vcpus` | no | Common requested CPU count where supported |
| `memory` | no | Common requested memory where supported |
| `env` | no | Guest environment map; secret references allowed |
| `depends_on` | no | Explicit dependency addresses |
| `link NAME` | no | Logical network attachment |
| `port` | no | Service exposure intent |
| `route` | no | Static guest route |
| `connection TYPE` | no | Guest connection parameters |
| `provisioner TYPE` | no | Guest execution or file copy |
| `provider TYPE` | no | Provider-specific configuration |
| `lifecycle` | no | Lifecycle policy |

<a id="hcl-link-name"></a>
#### `link NAME`

| Field | Required | Meaning |
|---|---:|---|
| `network` | yes | `sysbox_network` reference |
| `ip` | yes | Address with prefix |
| `gw` | no | Default/attachment gateway |
| `mac` | no | Stable declared MAC |
| `aliases` | no | Provider network aliases |

<a id="hcl-port"></a>
#### `port`

| Field | Required | Default | Meaning |
|---|---:|---|---|
| `target` | yes | | Guest port |
| `name` | no | | Logical service name |
| `published` | host exposure | | Host port |
| `protocol` | no | `tcp` | `tcp`, `udp`, `http`, `https` |
| `exposure` | no | `direct` | `none`, `direct`, or supported `host` |
| `host_ip` | no | provider default | Host bind address |

<a id="hcl-route"></a>
#### `route`

`dst` is a destination CIDR; `via` is the next-hop address.

<a id="hcl-connection-type"></a>
#### `connection TYPE`

Supports connection-specific `host`, `user`, `password` and `private_key`. Credentials should use secret references.
SSH connections verify host keys by default. Set `known_hosts` to an explicit trust store. `insecure_skip_host_key_check = true` is restricted to isolated, trusted labs and disables host identity verification.
Passwords and private keys should be declared in the connection block as secret references so they are resolved again for every execution, including after an Agent restart:

```hcl
connection "ssh" {
  user        = "ubuntu"
  password    = env("FC_SSH_PASSWORD")
  known_hosts = "/etc/sysbox/known_hosts"
}
```

<a id="hcl-provisioner-type"></a>
#### `provisioner TYPE`

Fields include `program`, `args`, `environment`, `working_dir`, `shell`, `source`, `destination` and `background`. Valid combinations depend on provisioner type. Provisioner operates inside the guest and does not own host resources.

`background = true` 的语义按 substrate 实现：docker 节点使用守护进程持有的 detached exec（进程归属与观察
都来自 Docker，不进入 guest helper 的签名任务协议）；VM/SSH/vsock guest 使用 `task.v1` 持久任务，需要
guest 内已安装 helper 与可写的 cgroup v2 根。未特权容器无法提供该 cgroup 根，所以 docker 不走这条路径。

<a id="hcl-docker-provider-块"></a>
### Docker Provider 块

```hcl
provider "docker" {
  privileged   = false
  pid_mode     = "host"
  cgroupns_mode = "host"
  binds        = ["./fixtures:/srv/fixtures:ro"]
  entrypoint   = ["/usr/local/bin/server"]
  command      = ["--listen", ":8080"]
}
```

Relative bind source resolves against the Sysbox process working directory. Omitted ENTRYPOINT/CMD inherits image config; non-empty array replaces it; explicit `[]` clears it. Values are direct argv, not shell strings.

<a id="hcl-firecracker-provider-块"></a>
### Firecracker Provider 块

| Field | Required | Meaning |
|---|---:|---|
| `kernel` | topology-dependent | Kernel resource/path |
| `rootfs` | topology-dependent | Rootfs resource/path |
| `chain_init` | no | Guest init chained by `sysbox-init` |
| `ssh_user` | no | Guest SSH user |
| `ssh_pass` | no | Secret reference recommended |
| `ssh_port` | no | Guest SSH port |
| `sysarmor` | no | Opt in to installing the pinned standalone SysArmor agent in this VM before declared provisioners; repeats after reset. Requires staged VM artifacts and a compatible Linux guest. Default `false`. |

Machine sizing and network initialization must match the selected provider capability and guest image.

Firecracker 正式运行只支持 jailer；`allow_direct` 已删除，旧 manifest v2 拒绝读取，按停机清理后重建升级。
宿主配置 `providers.firecracker.binary`、`jailer`、`jail_root` 和 `cgroup_parent`；不在节点 HCL 中配置 UID 或 jailer 参数。
当前固定使用 Firecracker/jailer **1.15.1**，可执行文件和祖先目录必须由 root 控制。
宿主服务以 root 准备资源，VMM 使用保留区间 **200000–265535** 内的 UID/GID；管理员应将该区间专用于 Sysbox。
UID 冲突会拒绝启动，不共享身份；清理后才释放占用。manifest 和授权记录不进入 jail。

宿主需支持 cgroup v2、`CLONE_INTO_CGROUP` 和 `cgroup.kill`，并提前建立 `cgroup_parent`，委派 cpu、memory、pids 控制器。
provider 不修改系统祖先 cgroup 的控制器设置。每次启动建立独立 cgroup：CPU 上限等于声明 vCPU 数，
内存上限为 guest RAM 加 128 MiB，禁止 swap，进程/线程数上限 64。当前 guest vCPU 数应低于该线程上限。
`jail_root` 应使用短路径，且与运行目录在同一文件系统，供本代磁盘建立硬链接；不映射整块宿主目录。
Stop 会强制停止并等待整个 VMM cgroup 清空，不等价于 guest 内的正常关机。

VMM 的 PID、mount、IPC、UTS namespace 隔离，权限与 seccomp 在启动后检查。
Firecracker 附件必须位于受管理的 named network namespace；宿主 Docker bridge 组合在附件创建和启动前拒绝。未连接网络的 VMM 使用独立 network namespace。
没有网络挂载的 VM 使用新的网络 namespace。jailer 不代替网络策略，也不把宿主 bridge 变成独立的网络隔离域。
启动准备中断可直接清理；已经启动但回复丢失时，用 cgroup、进程、jail root 和 API socket 证据恢复原 VMM。

<a id="hcl-libvirt-provider-块"></a>
### Libvirt Provider 块

| Field | Required | Meaning |
|---|---:|---|
| `network_init` | yes | Guest network initialization mode |
| `vcpus` | no | Domain vCPU count |
| `memory` | no | Domain memory |
| `machine_type` | no | Libvirt machine type |
| `disk_size` | no | Overlay/guest disk size |
| `ssh_user`, `ssh_pass`, `ssh_key` | no | Guest access |
| `ssh_authorized_key` | no | Key injected during guest init |
| `windows_etw` | no | Install the standalone Windows ETW collector before challenge provisioners and report observation source `etw`. Default `false`; requires `/var/lib/sysbox/vm-artifacts/windows-etw/` artifacts and a Windows guest. |

<a id="hcl-sysbox_router"></a>
### `sysbox_router`

Required `substrate` and `image`; repeated `interface NAME` blocks contain `network` and `ip`. Repeated `route` blocks contain `dst` and `via` for static guest routes. Optional `nat_from` and `nat_to` refer to interface labels. Router supports lifecycle policy.

<a id="hcl-sysbox_firewall"></a>
### `sysbox_firewall`

Top-level fields: `attach_to`, optional `family`, `default_input`, `default_output`, `default_forward`, and repeated `rule NAME`.

Rule fields:

- required `direction` and `verdict`;
- optional source/destination CIDRs and ports;
- optional protocol, input/output logical attachment and connection states;
- optional counter and log.

Current policy family is IPv4. Unsupported IPv6 input fails validation.

<a id="hcl-sysbox_ssh_access"></a>
### `sysbox_ssh_access`

Required `node` and `authorized_keys`; optional `bind_ip` and `port`. Authorized keys are sensitive execution input and must not leak into logs/state plaintext.

通过宿主 SSH dispatcher 和支持该能力的 guest helper 管理，使用 revision CAS、签名授权与停止回执。观察依赖 canonical node state；不支持的 transport 明确拒绝。配置字段见 [SSH resource handler](../pkg/runtime/resource_ssh_access.go)。

<a id="hcl-导入数据形状"></a>
### 导入数据形状

资源接管使用 [adoption 检查与确认](#cli-owning-agent-adoption-inspection)。只读检查不授予管理权；确认必须重新验证原始证据、归属和状态版本，不支持的 provider 场景明确拒绝。

<a id="cli"></a>
<a id="cli-cli-reference"></a>
## CLI 参考

本页对应当前 `sysbox --help`。具体版本以正在运行的 binary help 为准。

生命周期命令通过服务执行；本地维护命令与升级限制见[存储与恢复](#recovery-contract)。

<a id="cli-host-supervisor"></a>
### 宿主服务

```text
sysbox host-supervisor --config /etc/sysbox-host/sysbox.yaml --identity /etc/sysbox-host/identity.json
```

Serve signed Run lifecycle, guest task/file, power, SSH, adoption and GC operations over a local Unix socket. Requires root, an explicit
non-root worker group, private signing key and trusted canonical repository.
The Agent must set `agent.host_supervisor_socket`; these operations have no
direct-provider fallback. See [guest tasks](#host-protocol) for configuration,
recovery semantics and the remaining console and foreground execution boundaries.

<a id="cli-global-options"></a>
### 全局选项

| Flag | Default | Meaning |
|---|---|---|
| `-f, --file PATH` | `field.sysbox.hcl` | HCL topology file |
| `--state PATH_OR_URL` | `.sysbox/runs/default/state.json` | 仅本地维护命令使用；生命周期及 pause/resume 拒绝 |
| `--backend URL` | empty | 本地维护命令的存储位置；生命周期命令拒绝 |
| `--auto-approve` | false | Skip mutation confirmation |
| `--allow-unsafe-state` | false | Legacy flag; cannot bypass the atomic lifecycle repository requirement |

<a id="cli-workspace-and-validation"></a>
### 配置与验证

<a id="cli-init"></a>
#### `init`

Initialize a new Sysbox workspace.

<a id="cli-validate"></a>
#### `validate`

```text
sysbox validate
```

Parse and validate HCL without contacting a provider.

<a id="cli-plan"></a>
#### `plan`

```text
sysbox plan --api URL --topology NAME [-f FILE | --revision SHA256] [--include PATH] [--inputs FILE]
```

在服务上比较所选配置版本和正式 state，不访问本机 provider，也不切换当前 workspace。
不支持 `--refresh`。宿主在 Apply 规划时观察选定的受管资源，执行前再次核对；依赖替换范围不完整时拒绝，不自动扩大删除范围。

<a id="cli-mutation"></a>
### 生命周期操作

<a id="cli-apply"></a>
#### `apply`

```text
sysbox apply --topology NAME [-f FILE | --revision SHA256] [--include PATH] [--inputs FILE] [--target ADDRESS] [--key KEY] [--deadline RFC3339] [--auto-approve] [--detach]
```

<a id="cli-reset"></a>
#### `reset`

```text
sysbox reset --topology NAME [-f FILE | --revision SHA256] [--target sysbox_node.NAME] [--key KEY] [--auto-approve] [--detach]
sysbox reset --topology NAME --resume RUN_ID [--key KEY] [--auto-approve] [--detach]
```

续接使用原 Run 的配置和完整范围，不能同时传 file/revision/include/target。

<a id="cli-destroy"></a>
#### `destroy`

```text
sysbox destroy --topology NAME [--key KEY] [--auto-approve] [--detach]
```

以上命令共用 `--api`（默认 SYSBOX_API_URL 或 http://127.0.0.1:9876）、`--token`（默认 SYSBOX_API_TOKEN）。
它们调用服务的同一条执行路径；服务不可用就失败，不尝试本机执行。`--state`、`--backend`、`--allow-unsafe-state` 明确拒绝。
`--auto-approve` 确认显示的操作、revision 和 target；没有它时需要终端输入 yes。它不是绑定某次 Plan 的批准文件。

`--include` 可重复，路径相对于主 HCL 所在目录；只上传指定文件/目录，主文件在 revision 内命名为 field.sysbox.hcl。
不自动扫描整个项目，拒绝 symlink、隐藏文件、非 UTF-8 和超过 8 MiB 的 JSON 请求。附带脚本/模块需显式选择。
`--inputs` 是字符串值的 JSON 对象，不写进 revision/state；若 include 覆盖该文件或其硬链接则拒绝请求。

target 暂只支持未索引的根资源地址，例如 sysbox_node.web、sysbox_ssh_access.admin；模块/count/for_each 实例明确拒绝。
同名模块资源不会被根资源 target 选中。配置依赖必须已满足，不把 target 当完整部署的替代品。

请求前打印 key。回复丢失时使用完全相同的 key、revision、输入和范围重试；更改请求会返回 409。
主动发起下一次 Apply/Reset/Destroy 使用新 key。默认等待正式 Run 结果，`--detach` 在受理后返回 Run ID。

```text
sysbox run show RUN_ID --api URL
sysbox run wait RUN_ID --api URL
sysbox run cancel RUN_ID --api URL
```

Ctrl-C 只退出客户端等待，宿主已受理的 Run 继续执行。cancel 请求原 worker 命令取消；必须再查询结果，不能把 202 当成资源已停止。
服务重启导致 transient inputs 丢失时，重新提交原 key 和输入以恢复输入缓存，不新建操作。

<a id="cli-import"></a>
#### `import`

本地 `sysbox import ADDRESS EXTERNAL_ID` 已停用。接管现有资源使用
[owning-agent inspection](#cli-owning-agent-adoption-inspection)及 API adoption plan/confirm，不能直接写 state。

<a id="cli-runtime-control"></a>
### 运行控制

<a id="cli-pause--resume"></a>
#### `pause` / `resume`

```text
sysbox pause sysbox_node.web --topology NAME [--api URL] [--key KEY] [--detach]
sysbox resume sysbox_node.web --topology NAME [--api URL] [--key KEY] [--detach]
```

CLI 通过服务调用签名宿主操作，不打开本机 provider 或 state。只接受未索引的根 sysbox_node 地址；拒绝 --state/backend/allow-unsafe-state。
默认等待操作结果，--detach 在持久受理后返回 operation ID；Ctrl-C 只停止等待。接口查询 GET /v1/node-operations/{id}。
请求前打印 key；回复丢失时使用原 key 和原命令重试。已完成的旧 pause key 只返回原结果，不会把已 resume 的节点再次暂停。
同 topology 下相同 key 改换 node/operation/requester 返回 409；改变动作应使用新 key。
HTTP pause/resume 支持 Idempotency-Key；不改变 HCL 中的期望配置。

<a id="cli-show"></a>
#### `show`

Print one resource's state details as JSON.

<a id="cli-output"></a>
#### `output`

Print evaluated topology output values.

<a id="cli-state"></a>
### State

```text
sysbox state list
sysbox state show ADDRESS
sysbox state get ADDRESS[.ATTRIBUTE]
sysbox state mv SOURCE DESTINATION
sysbox state rm ADDRESS
```

- `list` lists canonical addresses.
- `show` prints full public instance attributes.
- `get` prints a resource or one attribute.
- `mv` renames logical state identity without touching the external object.
- `rm` forgets state and does **not** destroy the real object.

Quote addresses containing string keys:

```bash
sysbox state show 'module.lab.sysbox_node.web["blue"]'
```

<a id="cli-service-commands"></a>
### 服务命令

<a id="cli-serve"></a>
#### `serve`

Start the HTTP API server using the selected Sysbox config.

<a id="cli-agent"></a>
#### `agent`

Manage or run the host Agent. Use `sysbox agent --help` for the version-specific registration and execution subcommands.

<a id="cli-other-commands"></a>
### 其他命令

- `version [--json]`: build version, commit, time and Go version.
- `completion`: shell completion generation.
- `help`: command help.

<a id="cli-safety-notes"></a>
### 安全约束

- Mutation rejects backends without locking and CAS unless `--allow-unsafe-state` is explicit.
- `state rm` is not cleanup; it removes the ownership record needed for safe destroy.
- A stale stored plan or changed state serial fails before provider mutation.
- Configuration diagnostics, state conflicts and provider failures all return non-zero status; automation should use structured API diagnostics where available rather than matching prose.

<a id="cli-gc-候选提交"></a>
### GC 候选提交

`sysbox gc submit plan.json --api http://127.0.0.1:9876 --key cleanup-1`
提交一个明确候选，不直接访问 provider。API token 可通过 `SYSBOX_API_TOKEN`
提供。`plan.json` 为 `{"topology":"lab","candidate":{...}}`；candidate 包含
kind（container/network/domain/firecracker）、external_id、topology_uid、owner_uid、
resource_uid、generation、inventory_digest。摘要来自 canonical lifecycle references；
实际 provider 身份仍由宿主删除前核验。只使用完整盘点得到的候选，不把显示名当外部 ID。

输出为持久受理回执，不表示删除完成。同 key/同候选重试返回原操作；改参冲突。
API 与执行宿主必须消费同一 canonical reservation。宿主失联或未知删除结果保持冻结，
不能用新 key 绕过。已有 owning-host inventory 与批量 GC 路径；真实验收范围与当前发布状态见架构文档。

<a id="cli-owning-agent-adoption-inspection"></a>
### Owning-agent adoption inspection

`sysbox import inspect sysbox_node.web <external-id> --api <url> --topology lab --substrate docker --key inspect-1`
持久派发只读检查，返回 NodeOperation。等待该操作完成并核对证据后，再执行
`sysbox import confirm <inspection-id> --api <url> --topology lab --key adopt-1`。
确认只消费保存的宿主签名证据，不能在命令行替换 provider 证据或外部 ID。
普通 `sysbox import` 的本地直接 state 写入仍已移除。

检查成功不等于认领成功。Provider 在认领时再次比较指纹；拓扑 serial 或归属变化
会拒绝。已受管 Firecracker/libvirt 的恢复使用证据中的原 UID/generation，其他
topology 的归属不能转移。无主 libvirt 域的普通导入仍未实现，不能视为完成支持。

<a id="cli-complete-local-inventory-and-bounded-gc-batches"></a>
### Complete local inventory and bounded GC batches

`sysbox gc plan --runs <dir> --output gc-plan.json` inventories Docker containers /
networks, libvirt domains and Firecracker manifests against every canonical
filesystem aggregate under the selected directory, including retained journals
and snapshots. It runs on the resource host. Use `--catalog <sqlite-or-postgres-url>` to enumerate every topology in a shared
canonical database instead. Credentials are not written to the plan; a catalog
fingerprint prevents mixing earlier observations from another backend. A fixed
topology selector or templated storage path is rejected as an incomplete catalog.
Other runs directories, databases and execution hosts remain outside the selected
scope and cannot be implicitly covered.
A failed provider query or corrupt canonical inventory rejects the plan, rather
than producing deletion candidates from a partial result. Unknown owners are
reported as excluded and never adopted or deleted.

Plans retain raw inventory plus selected candidates. Default selection requires
an earlier complete `--previous-plan <file>` at least `--min-age 24h` old containing
the same owner/resource/generation/external identity. A first plan is an inventory
baseline with no age-qualified candidates. `--min-age 0` explicitly selects newly
observed candidates. `--limit` defaults to32 and is capped at128. Nodes/VMs precede
networks. Any retained resource UID or matching provider external ID protects the
object. This is an observation interval policy, not a claim about creation time.

`sysbox gc batch-submit gc-plan.json --api <url> --topology lab --key batch-1`
submits one topology's selected batch. The latest plan must be complete and no
more than15 minutes old. API admission revalidates references and actual host
ownership, so possession of a plan is not authorization by itself. A retry keeps
the same plan/key. Batches are durably reserved atomically, execute one item at a
time, and retain exclusion after unknown effects. No local raw deletion is used.

The API `GET /v1/topologies/{name}/recovery/intent-only` exposes a read-only
historical disposition report with journal hashes and evidence blockers. It does
not authorize forced success or arbitrary old-request replay.

`sysbox state audit-intents <aggregate.json>` reads an archived aggregate or host-effect-v2 ledger
without opening its backend, migrating it or changing version fields. It emits
source and per-record SHA256 plus minimal action identifiers; provider-private
state and credentials are not returned. Keep the original source alongside the
audit. This provides reproducible incident triage, not a new execution receipt.


<a id="cli-isolated-host-fencing-acceptance"></a>
### Isolated host fencing acceptance

`python3 scripts/acceptance/host_fencing.py create --root <new-lab> --base <pinned-cloud-image>`
creates a fresh private two-VM test lab. Run `prepare`, then `fence`. The script
only accepts a lab with an explicit disposable scope marker. It fingerprints the
exact execution QEMU and disk, uses pidfd/QMP fencing, verifies a new execution
boot ID and unchanged control boot ID, and checks local Docker/libvirt canaries.
Its proof distinguishes `host process exclusion` from `operation_result=unknown`.
It never targets an existing lab or historical resource. Remote daemon jobs,
external storage, migrated domains and business-result success require separate
proofs and remain outside this acceptance command.

<a id="cli-signed-disposable-fencing-evidence"></a>
#### Signed disposable fencing evidence

`sysbox host-fence request-digest <admission.json>` validates the API admission
record and prints its request digest. On the trusted external controller,
`sysbox host-fence attest <admission.json> <evidence.json> --key <controller.key>`
validates the exact request binding and local-provider acceptance evidence, then
prints an Ed25519-signed receipt. The key must be a private regular file containing
64 raw bytes and match the admission's pinned public key. Protect the controller
and its input files: the signer validates evidence structure and identity; it
cannot independently establish the physical truth of arbitrary JSON assertions.

For an already scoped disposable acceptance lab, pass `--admission`, `--attestor`
and `--controller-key` together to `host_fencing.py fence`. Before canary creation
or power-off the script checks the lab, original execution boot and controller
key. It snapshots the admission and produces `.receipt.json` only after pidfd
exit, new kernel boot, unchanged external controller boot and provider readback.
Submit that receipt to the API endpoint documented in [API](#http-api). Existing
unsigned reports lack this pre-actuation binding and cannot be attested by this
flow. Receipt acceptance leaves the persistent host hold in place. The API explicit
release endpoint is available only after independently completed deletion,
verified absent observation and all original grant stops; see [API](#http-api).

<a id="cli-scoped-cache-gc"></a>
#### Scoped cache GC

`sysbox gc cache --catalog sqlite:///absolute/catalog.db --cache-root /absolute/private/cache --path <64-hex-key>/<file>`
outputs a JSON plan without deleting. Save that plan and use
`sysbox gc cache --catalog sqlite:///absolute/catalog.db --apply-plan plan.json`
to remove the exact file after revalidation. This requires a private, current-user
owned cache exclusively used by writers enrolled in this catalog. Stop and enroll
old writers before use. The whole catalog is frozen through deletion, including
new topology insertions. Filesystem catalogs, topology-selected catalogs,
unregistered/incomplete/expired sources, symlinks, hardlinks, writable targets,
retained references and stale file/catalog identities are rejected.

PostgreSQL apply commits a durable reservation before local unlink. A database
statement trigger blocks lifecycle INSERT, UPDATE, DELETE and TRUNCATE while any
reservation is pending, including statements issued by older writers. This
protection survives connection/process loss. The trigger requires READ COMMITTED
isolation; transactions using snapshot isolation are refused. The executing
role needs permission to install the table, function and trigger on first use.
Only trusted operators may alter these guards or write the reservation table.

The cache root retains `.sysbox-gc/<id>.intent` before unlink and `.done` afterward.
A pending reservation has no timeout. If local deletion finished but database
release failed, run `sysbox gc cache --catalog <postgres-url> --reconcile-plan
plan.json` on the same machine. It checks the exact private local done receipt,
absence of the intent and target, and the database's machine/plan binding before
releasing the reservation. It never unlinks or replays deletion. Missing or partial
receipts require investigation and leave all catalog writers blocked. A receipt
does not authorize deleting a recreated file. Each invocation handles one private
host-cache file, never recursive directories or arbitrary shared/global caches.

For an interrupted unlink with an exact `.intent` but no `.done`, use
`sysbox gc cache --catalog <url> --recover-intent-plan plan.json` on the original
host. It acquires the same exclusive deletion lock, verifies the private journal
and exact PostgreSQL host/plan binding, requires the target to be absent, syncs
its parent directory and promotes the intent to a durable completion receipt.
It then releases the database reservation. It never performs unlink. A present
or replaced file, missing/partial intent, unsafe path, foreign host, or still-held
lock prevents recovery. This differs from `--reconcile-plan`, which requires an
already completed receipt. These operations are mutually exclusive with apply.

<a id="cli-cache-intent-disposition"></a>
#### Cache intent disposition

Use `gc cache --catalog <url> --inspect-plan plan.json` first. Its machine-readable
`action` is `wait`, `recover`, `reconcile`, `abort`, or `manual_review`. Inspection
acquires the original deletion lock without waiting and does not alter receipts.
`--abort-plan plan.json` preserves the exact original inode/content, durably
archives the intent as `.aborted`, and releases the PostgreSQL reservation with
`outcome=aborted`. It never claims resource deletion. A PostgreSQL reservation
committed before any intent may also be aborted, but only with exact original
host/plan binding, original file identity and the host lock. A recreated/modified
file is not eligible. Applying the old plan after abort is rejected.

For `manual_review`, preserve the plan, all `.sysbox-gc/<id>.*` files, target
stat/hash, and (for PostgreSQL) the exact reservation row before intervention.
Do not remove a lock, edit a reservation, synthesize a receipt, or retry unlink.
Identify the original machine and stop the exact original deleter; acquire its
lock through inspection. If the original target or intact original journal can
be recovered from retained evidence, restore that evidence on the original host
and inspect again. Then use the indicated exact abort/recover/reconcile command.
If identity or journal authenticity cannot be established, keep the reservation
quarantined and migrate new work to a separately enrolled cache/catalog; retain
the old catalog as evidence. This is the supported terminal manual disposition,
not a force-unfreeze operation. Foreign host, corrupted receipts, unsafe paths,
and conflicting terminal records must never be accepted as deletion success.

Cache planning includes global revisions in the same SQL catalog, even before
binding to a topology. Revision content is decoded and its tree digest verified;
embedded-file hashes and text references conservatively protect cache entries.
Inventory is bounded to 10,000 revisions / 64 MiB of encoded records; exceeding
limits rejects the plan. PostgreSQL apply requires the global revision table to
already exist and fences writes to it for the lifetime of the reservation.
Independent registries and filesystem global revision stores are not implied to
be covered by cache deletion. Cache GC never deletes revision objects; the API
provides a separate, explicit single-object SQL revision retirement endpoint.

<a id="http-api"></a>
<a id="http-api-sysbox-api"></a>
## HTTP API

HTTP API 位于 `/v1`，错误响应中的 `error` 为字符串。配置验证失败返回 HTTP 422 和 `diagnostics` 数组。
Plan 使用 canonical `address`，是配置/state 预览；宿主执行计划另行绑定资源身份并观察实际对象。
资源状态为 `present/absent/drifted/degraded/unknown`；API 不返回 driver-private payload 或已解析秘密值。

Run 完成仅接受宿主签名回执，Agent 事件不决定成功。回执、观察与恢复规则统一见
[宿主协议](#host-protocol)和[恢复契约](#recovery-contract)。
`GET /v1/agents/{agent_id}/projections` 来自持久化宿主清单；过期健康为 unknown，不由 Run 完成回调刷新。

Run claim requests must include `queued_at` and `revision` from the dispatched Run snapshot. Agents send a canonical nonzero UUID `claim_id`, reused only for transport retries of that execution. A live same-identity retry returns the committed lease without extending it or incrementing attempt; another identity, expired lease, or old incarnation is rejected. Claim bodies are limited to 1024 bytes before signature verification. The capability `claim_replay` advertises this guarantee. Empty claim bodies return 400; a snapshot from a previous execution returns 409. Command IDs distinguish execution incarnations even when a Run ID is reused. Upgrade API and Agent together.

Agent 领取 Run 后，把签名授权交给宿主监督进程。宿主从 canonical repository 读取原 Run 和不可变 revision，核对身份与整棵文件树摘要后物化到宿主私有目录；恢复使用相同逻辑。
`SnapshotPath` 是 canonical 记录中的绑定信息，不能由 Agent 用来指定宿主路径。
`POST /v1/agents/{agent}/runs/{id}/revision` 是带当前领取证明的只读 revision 查询，执行不依赖 Agent 下载或缓存文件。
Destroy 同样绑定 revision；仅按 state 回收时使用明确的空配置树。

二进制 artifact 使用 revision 内相对路径或带 SHA-256 的 HTTP(S) URL，执行宿主验证并缓存内容。
拒绝把 API 机器的绝对路径、目录穿越或不受控 symlink 当作远程输入。OCI 引用沿用 provider 的解析约定。

Apply accepts `Idempotency-Key` (1–128 ASCII letters, digits, colon, underscore or hyphen) with an immutable global `revision`. The key is scoped to topology and operation. Replaying the same request returns the original Run, including terminal results; changing revision, inputs, explicit agent, unsafe flag or deadline returns 409. A new execution needs a new key. Competing active Runs for one topology are rejected in the store transaction on all three backends.

Keyed Apply pins its immutable snapshot without rewriting the mutable workspace. Preflight and execution use that snapshot. Inputs stay in memory: after restart, `GET /v1/runs/{id}` reports `inputs_required: true` until the original request is resubmitted to the Agent-connected API. Claim returns 503 before taking a Run lease; Agent retries 503 with the original queued_at/revision. Successful/cancelled/nonrecoverable terminal replay does not restore secrets. Failed recoverable keyed Apply can explicitly rehydrate inputs for recovery only; it does not become executable again. Durable Run query errors return 503, authoritative absence404, stale claim/completion409. Nonempty revision labels that are not published global revisions now return400.

Authenticated `GET /v1/capabilities` includes protocol `sysbox-runtime-v1`, `durable_apply`, `keyed_destroy`, `cleanup_inspection`, `single_api_execution`, `external_absence_inspection` and `resource_lease_revocation`. The last two remain false: no unconditional absence proof or cessation from lease expiry. Signed host observation/fencing still requires actual evidence.

Additional fields: multi_api_execution is true only with an initialized PostgreSQL store; execution_store_scope is shared-postgres or host-local (file/SQLite). single_api_execution means single-instance operation is supported, not required. recovery_inputs_protocol=1 announces transient input handoff; keyed recovery also requires owning-Agent run-recovery-inputs.v1 and host inputs_protocol=1.

`GET /v1/topologies/{name}/cleanup-inspection` returns redacted `resources`, `unfinished_actions`, `active_operations`, `state_exists`, `metadata_exists`, source/time and scope. State+journal are one snapshot; operation lists are separate snapshots and cover only the reported `active_operation_kinds` (Run and NodeOperation). Remote ownership reports `owning_agent_not_observed`; `external_presence` is `unknown`. Empty lists never authorize cleanup. Storage errors return503. Use owning-agent Observe/Recover for actual provider evidence; metadata deletion still checks repository serial/owner and unfinished actions.

Workspace responses can include `active_run_id`, `active_revision` and `placement_agent_id`. A remotely placed topology remains listed after its Run ends. If the API has neither local resource evidence nor a mutable HCL pointer, phase is `unknown` with `OwningAgentNotObserved`, not `gone`. Deleting placement prevents retained terminal Run history from recreating the catalog entry.

The owning host may include a `cleanup` receipt with a successful whole-topology Destroy completion. It binds version1, topology/UID, AgentID, RunID/queued_at/revision, attempt/lease_owner, nonzero state_serial, observed_at and empty=true. The host proves empty managed state and no unfinished action from one repository snapshot; fresh/missing state cannot produce a receipt. Completion validates and persists the receipt atomically with the current lease; terminal retries must carry the identical receipt. Run queries return the stored `cleanup` field.

Firecracker guest transport separates `ping` liveness from `ready.v1` initialization. `ready.v1` is required before exec or file-write effects: systemd guests wait for both `sysinit.target` and successful `systemd-tmpfiles-setup.service`; custom init guests must write `/run/sysbox/boot-ready` containing the current boot ID after their own initialization. Console uses the signed task helper and requires its isolation prerequisites.

Ordinary metadata DELETE accepts only the latest successful Destroy receipt matching current placement and no subsequent lifecycle operation. New Runs, later/uncertain NodeOperations or placement rebinding invalidate it; missing proof returns409. Cleanup inspection reports `cleanup_receipt`, `cleanup_receipt_status` and its source as recorded evidence, not a live global absence query. Explicit `force=true` removes metadata only, never remote resources; active Run/NodeOperation or unfinished local journal blocks deletion in both modes.

Node-operation completion requires `action_id`, `resource_uid`, `generation`, `lease_epoch`, `lease_owner`, and `attempt` from the delivered command. The store checks the current command lease and commits the operation result in one lock/transaction. Expired or replaced leases return 409; identical committed terminal reports remain idempotent.

Guest execution/file metadata share one persistence contract. Guest endpoints distinguish missing records (404), conflicts (409), and corrupt/unavailable storage (500), with the existing string `error` envelope. Request, agent and command bindings remain private. The on-disk `record_format` is not an HTTP field; old metadata requires an explicit rebuild as described in the [storage contract](#recovery-contract).

```bash
GET /v1/schema
```

<a id="http-api-projects"></a>
### Projects

```bash
GET /v1/projects
GET /v1/projects/default
GET /v1/projects/default/workspaces
```

<a id="http-api-agents"></a>
### Agents

Agent 是普通用户通信进程，执行由独立 host-supervisor 负责。
API 保存 Agent 身份、管理员注册的能力与标签、禁用状态和心跳。
只有 API 与宿主监督进程访问 canonical state；Agent 不打开数据库或 provider socket。

```bash
GET  /v1/agents
POST /v1/agents
GET  /v1/agents/{agent_id}
POST /v1/agents/{agent_id}/heartbeat
GET  /v1/agents/{agent_id}/commands
GET  /v1/agents/{agent_id}/commands/stream
POST /v1/agents/{agent_id}/commands/{command_id}/cancel
GET  /v1/agents/{agent_id}/command-events
GET  /v1/agents/{agent_id}/projections
GET  /v1/agents/{agent_id}/inventory
POST /v1/agents/{agent_id}/observations
POST /v1/agents/{agent_id}/runs/{run_id}/claim
POST /v1/agents/{agent_id}/runs/{run_id}/renew
POST /v1/hosts/{agent_id}/runs/{run_id}/completed
```

管理员用 `sysbox agent register --api <URL> --token <管理员凭据> --id host-a --substrates docker --identity <路径>` 注册。
注册后保存的身份文件不含管理员 token，只含本 Agent 的通信凭据。运行时重连和 heartbeat 不能改写能力或标签。

<a id="http-api-topologies-and-workspaces"></a>
### Topologies And Workspaces

Topologies are created implicitly by applying a published revision: the API
materializes the revision's HCL into the workspace (upsert) and dispatches a
run.

```bash
POST /v1/revisions

GET    /v1/topologies
GET    /v1/topologies/{name}
GET    /v1/topologies/{name}/hcl
DELETE /v1/topologies/{name}

GET  /v1/topologies/{name}/plan

POST /v1/topologies/{name}/apply
POST /v1/topologies/{name}/reset
POST /v1/topologies/{name}/destroy
```

`POST /v1/revisions` accepts raw HCL as the request body and returns a
content-addressed digest: `{"revision":"sha256:<hex>"}`. Publishing the same
HCL returns the same digest.

`POST /v1/topologies/{name}/apply` upserts the workspace with the revision's
HCL and dispatches a run. The request body is:

```json
{"revision":"sha256:<hex>","inputs":{},"deadline_at":"2026-09-06T00:00:00Z"}
```

Keyed Apply (`Idempotency-Key` with an immutable global `revision`)
deliberately does **not** publish a mutable workspace pointer, so `has_hcl`
stays false. That flag is not an existence check: a topology may exist from a
committed lifecycle aggregate, an in-flight Run, or a placement. Consumers must
treat a topology as present when `has_state` is true, `active_run_id` is set, or
`placement_agent_id` is set, and absent only from `phase=gone` after metadata
deletion. The supervisor enumerates the union of workspace HCL directories,
committed lifecycle aggregates, and Run records, so an HCL-less keyed topology
with committed state is health-scanned and reported normally.

`inputs` are materialized per-run and never returned by the API. `deadline_at`
bounds convergence. `GET /v1/topologies/{name}` returns workspace metadata and
`status` with `phase`, `conditions` (`Applied`, `Provisioned`, `Asserted`,
`Ready`), `nodes`, `outputs`, and `deadline_at`. `Applied` is derived from the
topology's latest Run verdict; a completed destroy is not an apply verdict (a
failed destroy keeps its failure verdict) and projects `phase=destroyed`:
resources were released, but the workspace and lifecycle metadata are retained
for audit and cleanup. `phase=gone` remains authoritative absence (no
workspace, no committed state, no in-flight Run, no placement); it is not the
same state as `destroyed`. `Provisioned` is the supervisor's health observation.
`Asserted` is the latest check/assert outcome, and `Ready` is the conjunction of
the three (Unknown propagates, False dominates). Apply/repair and reset both
evaluate the scenario's checks after execution, so a completed reset returns a
fresh `Asserted` verdict rather than leaving it Unknown.

<a id="http-api-state-and-observability"></a>
### State And Observability

```bash
GET  /v1/topologies/{name}/state
GET  /v1/topologies/{name}/state/metadata
GET  /v1/topologies/{name}/state/lock
POST /v1/topologies/{name}/state/force-unlock
GET  /v1/topologies/{name}/state/snapshots
POST /v1/topologies/{name}/state/snapshots/{snapshot}/restore

GET /v1/topologies/{name}/outputs
GET /v1/topologies/{name}/preflight
GET /v1/topologies/{name}/stack-state
GET /v1/topologies/{name}/lease
GET /v1/topologies/{name}/snapshots
GET /v1/topologies/{name}/health
GET /v1/topologies/{name}/resources
GET /v1/topologies/{name}/status/stream
GET /v1/topologies/{name}/resources/{resource}/health
```

Canonical aggregates support snapshots and the explicit restore-plan/restore workflow.
`GET .../state/lock` reports execution-authority grants and maintenance barriers,
including revoked or expired executions without confirmed host stops. It does not
claim to inspect transient database mutexes. `POST .../state/force-unlock` with an
`Idempotency-Key` persistently revokes authority and returns 202 `waiting_for_stop`
until activity and host-stop evidence allow release; retry the same key after
restart. 200 `unlocked` includes the original durable barrier/audit identity and
does not remove flock files. Calls without a key retain the conservative legacy
behavior; canonical backend raw force-unlock is unsupported.

<a id="http-api-runs"></a>
### Runs

```bash
GET  /v1/runs
GET  /v1/runs/{run_id}
GET  /v1/runs/{run_id}/logs
GET  /v1/runs/{run_id}/checkpoint
GET  /v1/runs/{run_id}/actions
GET  /v1/runs/{run_id}/events
POST /v1/runs/{run_id}/resume
```

Run records include the owning `agent_id`, protocol version, lease owner, lease
expiry, and attempt count. The API creates and assigns command intent; agents
keep an outbound WebSocket command stream open at
`/v1/agents/{agent_id}/commands/stream`. Commands use a durable envelope with
`id`, `type`, `protocol`, and command-specific payload. Agents ACK, start,
complete, and fail commands on the same WebSocket; command events are persisted
and available from `/v1/agents/{agent_id}/command-events`. Commands are
persisted before delivery, leased with an atomic store operation before
WebSocket delivery, replayed to reconnecting agents after lease expiry, and can
be cancelled with `cancel_command`. Postgres stores command/run lease owner,
lease expiry, and attempt count so multiple API replicas do not deliver or
claim the same work concurrently. The legacy SSE command stream has been
removed. When a run is assigned, the API pushes a `run_assigned` command; the
agent then claims the run through the same lease/attempt model and relays its signed authorization
to the host supervisor. While a run is active, the agent renews its run lease through
`/v1/agents/{agent_id}/runs/{run_id}/renew`; expired running leases are marked
failed/recoverable by the supervisor. The host signer owns final Run completion;
Agent progress events cannot substitute for its receipt.

```text
queued -> assigned -> running -> done|failed|cancelled
```

Agents also enforce a local host policy before executing commands. Configure
`agent.policy.allowed_workspaces`, `allowed_substrates`, `allowed_commands`,
`allow_console`, and `allow_import` in `sysbox.yaml` to keep host-local
capabilities scoped even if the control plane is misconfigured.

Registered agents are persisted in the API store. They have a per-agent secret,
`secret_hash`, protocol version, capabilities, heartbeat, and operational
status. API responses expose only `secret_hash`; agent-originated heartbeat,
inventory, projection, run completion, console attach, and command stream
requests are signed with `X-Sysbox-Agent-*` headers.
`PATCH /v1/agents/{agent_id}` persists disabled/quarantined state; disabled or
quarantined agents are skipped by scheduling and cannot open the command stream.
Agents whose heartbeat becomes stale are marked `offline` by the supervisor and
will not be selected until a fresh heartbeat restores them to `online`.

Agent 周期性转发宿主签名快照到 `/v1/agents/{agent_id}/observations`。
能力、容量、拓扑归属与健康由宿主采集，API 校验当前 session 和宿主分配的递增序号；1 分钟后快照过期。

<a id="http-api-console-sessions"></a>
### Console Sessions

The API admits an exact node generation and relays WebSocket frames. The Agent
forwards to the host supervisor; the existing signed guest task engine runs the
terminal as UID65534. There is no root/native console fallback. Linux guests
require the trusted helper, issuer public key and writable cgroup-v2 domain;
TTY sessions also require PTY support. Windows terminals are unsupported.

```bash
POST /v1/topologies/{name}/nodes/{node}/sessions
GET  /v1/sessions/{session_id}
GET  /v1/sessions/{session_id}/attach
POST /v1/sessions/{session_id}/cancel
GET  /v1/agents/{agent_id}/sessions/{session_id}/attach
```

Browsers attach to `/v1/sessions/{session_id}/attach`. Agents attach to the
agent-side URL after receiving a `session_open` command on their command
stream.
Session metadata is persisted by the API store. `lost` means transport was
interrupted, not process termination. A signed host result sets `host_completion`
and `termination_confirmed` after all task descendants stop. Only a known exit
status supplies `exit_code`. Sessions proven never authorized use `not_started`
and do not invent a remote stop receipt. Final completion retires both delivery
commands; restart reconciliation resumes partial publication.
`timeout_seconds` is an absolute limit from session creation and cannot be
extended by grant renewal. Disconnect/cancel also requests task retirement.
The API uses its request subject for identity: the configured
user header (`X-Sysbox-User` by default) or `api`, and the configured roles header
(`X-Sysbox-Roles` by default). Body-supplied `requested_by` and `roles` cannot
override that identity. Console RBAC is configured in
`api.console.allowed_roles` plus `api.rbac.admin_roles`; when no console roles
are configured, local development remains permissive. Session records include
the policy name and audit events for create, allow/deny, attach, cancel,
timeout, and close. Creation, metadata reads, cancellation and browser attachment
all check the current Console role policy; denied existing-session requests
return 403 before data disclosure, state changes or WebSocket upgrade. Registry
failure returns a redacted 503; a missing session returns 404. Cancellation audit
records the acting subject's roles, not the creator's roles. Agent attachment
uses its separate agent identity and session assignment checks.

资源观察包含在同一份宿主签名快照中。UI 可通过
`/v1/topologies/{name}/status/stream` 订阅更新，并查询资源接口确认快照仍然有效。

WebSocket text frames use a small JSON envelope. Binary payloads are base64
encoded so the same envelope works across browser terminals and simple tools.

```json
{"type":"stdin","data":"bHMK"}
{"type":"resize","cols":120,"rows":40}
{"type":"eof"}
{"type":"stdout","data":"..."}
{"type":"stderr","data":"..."}
{"type":"exit","code":0}
```

`stdin` must be valid base64 and decode to at most 8192 bytes per frame. Window
dimensions are 1–65535. `eof` closes non-TTY input; for a PTY, send the application's
EOF character through `stdin`. Output is split into bounded frames, retains the
guest task's output quota, and stops the session on overflow. Input is not stored
in the durable task request. Output may echo typed input and is retained under
the task's normal output policy; terminals do not provide secret input redaction.

<a id="http-api-nodes"></a>
### Nodes

```bash
GET  /v1/topologies/{name}/nodes
GET  /v1/topologies/{name}/nodes/{node}
POST /v1/topologies/{name}/nodes/{node}/pause
POST /v1/topologies/{name}/nodes/{node}/resume
POST /v1/topologies/{name}/import
GET  /v1/node-operations/{operation_id}
```

Pause, resume, and import are agent-backed node operations. The API creates an
operation intent, assigns it to a capable agent, and the agent completes the
local substrate call before posting the final operation status back to the API.
Operation records persist requester, roles, status, and audit events so API/UI
users can explain who requested a host-local action and how it completed.

<a id="http-api-guest-operations"></a>
### Guest Operations

Guest operations address only a topology and logical node. Provider handles,
container IDs, VM UUIDs, host paths, credentials and Agent identity are never
part of the public response.

```text
POST /v1/topologies/{topology}/nodes/{node}/executions
GET  /v1/executions/{execution_id}
POST /v1/executions/{execution_id}/cancel
PUT  /v1/topologies/{topology}/nodes/{node}/files?path=/absolute/path&mode=0400
GET  /v1/file-operations/{operation_id}
POST /v1/file-operations/{operation_id}/cancel
```

Execution creation accepts `argv`, optional `environment`,
`working_directory`, and `timeout_seconds`, then returns `202 Accepted` with a
durable `queued` resource. The owning Agent reports `running`; the host's signed
receipt establishes `completed`, `failed`, or `cancelled`. A non-zero guest exit is `completed`
with `result_class: exit`; stdout and stderr are base64 encoded and bounded.
Durable execution currently uses the default 1 MiB bound per stream.
`output_limit_bytes` may be omitted, zero, or the default; custom bounds and
`capture_via_file` are rejected explicitly. The local Windows QGA adapter can
capture through files, but does not implement the durable Start/Observe/Cancel
protocol required by remote executions. Remote Windows ETW execution remains
unsupported until that protocol is available.
The private request and output payload expire 15 minutes after completion,
while the sanitized lifecycle metadata remains available.

Execution cancellation persists `cancel_requested: true` and keeps the current
queued/running status until an actual outcome is reported. Repeated requests
reuse one cancellation command; restart reconciliation repairs missing delivery
without resetting a live lease. A real completion may win the cancellation race
and retain its result. Cancellation blocks subsequent execution-grant issuance,
authorization and renewal, but does not itself prove remote termination.
Cancellation intent and its outbox commit together. SQLite/Postgres also revoke
issued grants in that transaction. File storage commits the intent and outbox
seed in one record, then publishes revocation; restart replay finishes that
publication even after task completion or delivery retirement. Notification
failure does not turn durable acceptance into an API failure. Host-signed
execution completion now carries the bounded result with the quiescence proof;
the API can finish an execution after worker lease expiry or restart. Missing
exit status is recorded as unknown rather than success.

The worker `/v1/agents/{agent}/guest-executions/{execution}/complete` route has
been removed. Host receipts use `/v1/hosts/{agent}/execution-grants/stopped`
and require `guest_result` with an explicit `exit_code_known` flag. Completion
is committed before releasing authority; exact replay survives output expiry
and successor epochs. Run-derived tasks release their child grant without
creating a separate API execution. Receipt requests are limited to 4 MiB;
stdout/stderr are base64 encoded and bounded to 1 MiB each before signing.

For a cancelled execution that never received execution authority, recovery
retires the original delivery, verifies the original admission and the complete
grant/stop history under the authority lock, then records `cancelled` with
`result_class: not_started`. This also works when an Agent already reported
`running`: that report alone does not prove remote execution. Any historical
grant requires host evidence instead. Interrupted cleanup resumes from the
durable cancellation intent without changing an established terminal result.

Supervisor recovery also handles failed, denied or cancelled deliveries when
the user did not request cancellation. It retires the delivery and verifies
the original admission plus complete grant/stop history in the same authority
transaction. Never-authorized tasks become `failed / not_started` without
setting `cancel_requested`. Previously authorized tasks retain their execution
state; their remaining grants are revoked and host evidence supplies the result.

`POST /v1/agents/{agent}/guest-task-stops` lets the owning host discover revoked
standalone and Run-derived guest tasks, using signed Agent authentication. A disabled Agent may
still drain effects; an identity without stored credentials is rejected. The
JSON cursor contains optional `after_epoch` and `through_epoch`. Start with `{}`;
continue with the returned `next_epoch` as `after_epoch` and keep the returned
`through_epoch` until `next_epoch` is zero. Each page has at most 64 entries
across `stops` and `issues`. Each stop carries topology/node names and a signed,
stop-only cancellation proof. Issues identify unavailable source records; they
do not prevent pagination. This route issues no execution authority.

A reboot stop receipt additionally contains `observed_guest_boot_id`, signed
alongside the original `guest_boot_id`. The host must first verify canonical
resource identity. A changed boot proves old-process termination only, so its
guest result is `unknown` without output or a known exit code. Terminal standalone
executions still rely on exact host-receipt replay to finish an interrupted
authority release.

For a Run child, the cancellation proof also signs `run_id` and `topology`.
The API verifies the parent grant and original Run command digest before
signing. The response leaves `node` empty: the host derives it from trusted
canonical state or an unfinished candidate belonging to the signed Run. A bad
parent source yields an issue, not cancellation authority. Parent revocation
atomically revokes unfinished children; stopped children remain stopped.
Acknowledging a child stop releases only that child, never the parent Run.

A file delivery command may be retired before its host receipt arrives. Completion
reconstructs the original request from the durable operation's dispatch seed,
checks the retired command's identity and scope, then verifies the original file
digest, size and mode. It never reconstructs the request from the returned result.

File upload uses the request body as raw binary bytes, `X-Content-SHA256` for
integrity, and query parameters for the absolute guest `path` and octal `mode`.
The limit is 16 MiB. Paths containing NUL, traversal, or a non-absolute path
are rejected. The API response contains a durable operation with `queued`,
`running`, `completed`, `failed`, or `cancelled` status. The operation can be
read and cancelled through `/v1/file-operations/{operation_id}`. Agent and
provider identifiers, host paths, credentials, and private payload references
are never returned. Payloads are staged with restricted permissions, bound to
the owning Agent, and retained for retry until the operation becomes terminal
or the 15-minute staging lifetime expires. Providers install content and mode
atomically where supported.

These endpoints require the dedicated guest-operation authorization check;
the owning Agent uses authenticated internal start and payload-fetch
endpoints; only host-signed evidence can complete the operation. Local Agent policy must explicitly allow `guest_execution` and/or
`guest_file_put`. Docker and Firecracker expose execution and file capability;
libvirt remains capability-gated.

<a id="http-api-artifacts-and-policies"></a>
### Artifacts And Policies

```bash
GET  /v1/artifacts
GET  /v1/policies
POST /v1/policies
```

<a id="http-api-guest-completion-evidence-format"></a>
#### Guest completion evidence format

Lifecycle aggregates use format 16 for all supported operations. Earlier formats are
rejected by normal lifecycle access and have no import command. Preserve their
original bytes separately, then rebuild a fresh current catalog; never edit version
fields manually.

The current API schema is 15. Host completion of a standalone guest task stores
its terminal result together with an immutable grant identity and receipt hash;
SQL backends commit result and authority release together. The file backend
replays that durable proof after an interrupted publication. Old execution
format 4 and API schema 13 data require explicit rebuild; there is no compatibility
reader. A replay never contacts the guest and cannot release a successor epoch.

Metadata deletion uses a durable `delete.intent` containing topology UID and
state serial. It is written before state reset and removed after workspace,
placement and health cleanup. A restart or supervisor scan resumes this intent;
new workspace publication is rejected while it exists. The marker does not
prove external resource absence; provider teardown must already have a valid
owning-agent cleanup receipt.

SSH access and adoption host completion also require the original authority grant
at the storage boundary; callers cannot bypass this by invoking a backend adapter
directly. Their operation record and authority release remain a separate
retryable commit boundary until the operation mutation is migrated to the shared
atomic completion adapter.

<a id="http-api-gc-dispatch-and-evidence"></a>
#### GC dispatch and evidence

`GET /v1/topologies/{name}/gc/references` returns the coherent lifecycle reference
digest and retained resource identities. It is not a provider inventory or deletion
authorization. `POST /v1/topologies/{name}/gc` takes one candidate (kind, external_id,
topology_uid, owner_uid, resource_uid, generation, inventory_digest) and requires
`Idempotency-Key`. It binds the existing owning-Agent placement, checks complete
activity and stopped authority under storage exclusion, and reserves the lifecycle
reference set before publishing `gc_delete`. 202 returns the reservation/command
identity. Same-key retries cannot change the candidate or restart a completed delete.

The reservation remains across lease expiry and process restart. Lifecycle,
snapshot and adoption writes cannot introduce references during deletion. The
host must read the identical reservation from its canonical repository and verify
exact provider identity and post-delete absence. Only a candidate-bound enrolled
host receipt can complete the reservation. Completion retires the command before
stopping authority and reopening writes; exact receipt replay repairs interrupted
completion. Worker terminal events and generic quiescence receipts do not prove
deletion. Unknown effects keep the reservation; automatic uncertain-intent
reconciliation and batch inventory/retention have dedicated contracts below; this single-candidate route does not imply full release acceptance.

<a id="http-api-owning-agent-adoption-inspection"></a>
#### Owning-agent adoption inspection

`POST /v1/topologies/{name}/adoption-inspections` accepts `type`, `name`, `id` and
`substrate`, requires `Idempotency-Key`, and returns202 with a durable
`adoption_inspect` NodeOperation. The owning host performs bounded read-only
inspection and signs the exact result. Successful operations expose
`inspection_evidence`; failed inspection is a known signed read-only failure.
Worker reports cannot supply or replace the host evidence. Source validation
rejects further grants once the operation is terminal.

`POST /v1/topologies/{name}/adoption-inspections/{inspection}/confirm` requires a
separate `Idempotency-Key` and consumes only the stored successful evidence. It
returns202 with the separate adoption operation. Current topology UID/serial,
placement and provider fingerprint are checked before mutation. Managed evidence
must belong to this topology; its existing resource UID/generation is preserved.
This supplies the public inspection/confirmation route, but does not add unowned
libvirt adoption or close the full P5 cold-fault matrix.

<a id="http-api-gc-batch-plans-and-historical-intents"></a>
#### GC batch plans and historical intents

`POST /v1/topologies/{name}/gc/batch` accepts 1–128 distinct candidates and an
`Idempotency-Key`. The entire normalized batch is reserved in one authority /
activity / aggregate transaction; a bad later candidate leaves no partial batch.
The key binds topology, owning Agent and all candidate identities. Changing,
truncating or appending candidates on replay conflicts. Containers and VMs precede
networks. Only the first unfinished reservation can receive execution authority;
its verified host completion publishes the next. Unknown outcomes stop progress
and keep all lifecycle references frozen. Batch metadata is stored in the current lifecycle format; see [storage versions](#recovery-contract).

`GET /v1/topologies/{name}/recovery/intent-only` reports unfinished journal actions
without provider effects or journal changes. Each entry includes the Run, exact
resource identity, journal revision/SHA256, quiescence and specific blockers.
Missing old operation anchors, unknown daemon calls, incomplete guest effects and
reset continuation requirements remain distinct. Returned evidence is classified
as requiring authorized observation; it is not automatically declared complete.
Unknown records are not force-cleared. This endpoint is a disposition report,
not physical host fencing or permission to replay an old request.

<a id="http-api-disposable-host-fencing-evidence"></a>
#### Disposable host fencing evidence

This opt-in interface records externally signed process-exclusion evidence. It
never reboots a host or completes/replays an interrupted operation. Enroll only
isolated disposable acceptance Agents in API configuration:

```yaml
api:
  execution_grant:
    disposable_fence_targets:
      disposable-agent:
        lab_id: sysbox-p9-example
        controller_public_key_file: /etc/sysbox/fence-controller.pub
```

The raw Ed25519 public key must differ from the API issuer and every enrolled
host key. Keep its private key exclusively on the external controller.

- `POST /v1/topologies/{name}/host-fences` requires `Idempotency-Key` and
  `{"run_id":"...","grant":{...}}`, containing the exact retained parent Run
  grant. It checks the original durable command, Run incarnation and production
  request digest, requires an anchored nonempty action journal, and returns202
  with a `HostFenceRecord`.
- `GET /v1/topologies/{name}/host-fences/{id}` reads that record after restart.
- `POST /v1/topologies/{name}/host-fences/{id}/receipt` accepts the signed receipt
  and returns200. Conflicting receipt retries or invalid signatures return409.

Admission atomically freezes the topology, revokes its active grants and places
a persistent hold on the Agent across topologies. Another non-stopped topology
grant on that Agent prevents admission. New grants, command delivery, dispatch,
and nonterminal Run claims/writes are blocked; observation, stop and completion
evidence remain available. The journal hash binds an observed snapshot, not an
atomic cross-repository recovery decision. Request replay uses its original
pinned controller key and journal hash, including after configuration rotation.

Receipts bind the request digest, lab, original/new execution kernel boots,
external controller boot continuity, original QEMU process exit, disk identity
and `operation_result=unknown`. Receipt acceptance does not synthesize a stop
receipt or change any Run/action/resource outcome. It leaves the hold intact;
ordinary maintenance release is rejected. The explicit release endpoint below supports
only independently reconciled empty delete topologies; it does not resolve
unknown effects. See [upgrade boundaries](#recovery-contract).

<a id="http-api-observation-after-a-host-fence"></a>
##### Observation after a host fence

`GET /v1/topologies/{name}/host-fences/{id}/recovery-plan` returns a read-only
triage plan. It checks the current original Run/journal against the admission
hash, distinguishes verified host process exclusion from provider operation
completion, and lists action identity/anchor, external identity, guest effect and
reset-continuation blockers. It does not create a missing lifecycle or write
Run/action/state records. `release_allowed` and `replay_allowed` remain false.

After a receipt is accepted, `POST .../host-fences/{id}/observe` with
`Idempotency-Key` dispatches `run_recovery` in **observe** mode to the original
owning Agent. No request body is needed. New requests require the original
journal snapshot to match and the Agent to advertise `run-recovery.v1`. The
request digest and key determine one durable command. Concurrent retries,
retries after delivery, and terminal retries return the same `command_id`
without resetting its lease or rerunning the command. An existing key remains
readable after journal drift; a new key is rejected. Use a new key only for a
new observation of the still-matching snapshot.

Read `GET .../recovery-plan?observation={command_id}` for the associated result.
The command must belong to this fence. The plan checks Run incarnation/attempt,
Agent, topology, state serial, observation time after fencing, and exact action
identity/coverage before displaying the reported presence. Status is `pending`,
`failed`, `matched`, `mismatched`, or `stale`; changed current metadata produces
`stale` with blockers, while a foreign command is rejected. The new endpoints
require an administrative role, like the existing remote recovery endpoints.

These reports pass through the owning host's read-only observation path and
normal command lease validation. For a fence-bound command, recovery authorization
includes `fence_observation` with the admitted request digest and the receipt's
new host boot ID. The host supervisor checks its own kernel boot, observes, then
revalidates the authorization before signing the complete report with its host
Ed25519 key. The optional `host_observation` proof also binds the command delivery
owner/attempt; the worker forwards it unchanged.

The plan verifies that proof with `api.execution_grant.host_public_key_files`
for the owning Agent, the exact fence request/new boot, and the retained command
lease. `observation_attestation=verified` removes only
`observation_not_host_boot_attested`. Unsigned reports remain `unverified` and
informational. A supplied but invalid signature produces `invalid`, retains a
`host_observation_signature_invalid` blocker, and exposes presence as unknown.
Missing/rotated host keys cannot establish verification; retain the enrolled key
when auditing existing evidence. A matching signed `absent` report cannot complete
a delete, replay a create, manufacture a stop receipt or release the host hold.
The signature proves who observed which data on which boot, not original operation
success or provider request quiescence.


<a id="http-api-explicit-release-after-completed-deletion"></a>
##### Explicit release after completed deletion

`POST /v1/topologies/{name}/host-fences/{id}/release` requires an administrative
role, `Idempotency-Key`, and `{"observation_command_id":"..."}`. It returns200
with an immutable release receipt only after **all** checks pass in the storage
transaction:

- The original destroy Run is terminal and its incarnation/attempt still matches
  the retired observation command.
- The current topology state is empty, every journal action is succeeded, no GC
  batch is pending, and no reset continuation remains. Every action belonging to
  the original Run is a completed delete with a retained external identity.
- The host-signed report matches the current topology UID/serial, full original
  delete-action coverage, exact external identities and new fence boot, and says
  absent for every selected action.
- The original parent grant has an independently authenticated stop receipt.
  All retained grants for this host or topology are stopped; expired or revoked
  grants are insufficient.
- No unresolved Run, guest operation, node operation, console session or command
  remains for the topology.

The endpoint releases the barrier and host hold in one authority write, retaining
all fence/observation/stop evidence. It does not modify state, action outcomes,
Run status or grant stops. Failed checks return409 without releasing anything.
File storage holds lifecycle exclusion through authority publication; SQLite and
PostgreSQL retain transaction locks through commit.

Same-key/same-observation retries return the original receipt, even after API
restart, key configuration changes or subsequent work. A different key or
observation conflicts. Old retries cannot release a later barrier. New observation
requests and reauthorization against the released fence are rejected. Plans expose
`release_status=held|released`; `release_allowed=false` on the read-only plan is
not a substitute for the atomic endpoint checks.

Release and reconciliation records require execution authority **format5**. There is no automatic
conversion from format3/4; see [upgrade boundaries](#recovery-contract).
The release endpoint itself does not reconcile an uncertain delete; the separate
transition below handles only the explicitly supported case. It cannot adopt an
uncertain create, resume an incomplete reset, or manufacture missing history.

<a id="http-api-reconcile-a-fenced-unknown-deletion"></a>
##### Reconcile a fenced unknown deletion

`POST /v1/topologies/{name}/host-fences/{id}/reconcile-delete` requires an
administrative role, `Idempotency-Key` and `{"observation_command_id":"..."}`.
This narrow transition applies only to explicitly enrolled disposable hosts and
anchored, topology-wide destroy Runs. It invokes no provider effects.

The original fence journal hash, Run incarnation/attempt, original command and
revoked parent grant must still match. Every selected action must be an unresolved
`sysbox_node` delete with its original operation ID, `action:{id}` anchor, exact
candidate and state identity, Docker/libvirt driver, and no attachments/reset
work. The host-signed report must cover all selected actions with `absent`,
`provider_scope=local-only` and `delete_residue_absent=true`. The actual Docker
client must use the default local Unix socket; libvirt must report `qemu:///system`.
Libvirt also checks that a managed VM directory is absent; externally adopted
storage remains intentionally preserved. Other outstanding grants, child effects,
remaining resources or unresolved topology activity block the transaction.

On success the response contains the durable reconciliation decision. The
transaction commits the delete outcomes and removes exactly the matching state
resources, retires the original command, and closes the interrupted Run as
**failed, nonrecoverable** with an explicit reconciliation reason. It does not
claim that the interrupted execution returned success. The parent grant stop
references the authenticated fence/reconciliation evidence (`fence-delete:`),
not a fabricated host stop signature. The host hold remains until an explicit
`release` request passes its activity checks.

Execution authority format5 records preimage/postimage hashes, pinned verifier
key, signed observation and decision time. SQL commits lifecycle, Run, command
and authority atomically. Filesystem storage persists a prepared decision before
postimages; retries accept only the original or planned snapshots, finish the
remaining writes, then publish completion/stop evidence. Run history uses atomic
replacement, avoiding a torn append that would prevent recovery. A pending decision
blocks new observations and never releases the hold. Same-key completed retries
return the original decision; another key/report conflicts.

Release after reconciliation consumes the retained signed **pre-reconciliation**
report plus the exact committed postimages. The signature is never rewritten to
pretend it observed the new journal status/serial. Plans expose
`reconciliation_status=none|prepared|completed`. A stale original-journal label
after reconciliation does not negate the separately retained decision.

This does not cover create/reset, absent original anchors, remote daemons, unknown
external storage effects, composite guest work or arbitrary historical incidents.

<a id="http-api-snapshot-preservation-and-bounded-retention"></a>
#### Snapshot preservation and bounded retention

Metadata restore may retain unresolved actions only when every unresolved entry
is identical in the snapshot and current journal in both directions. It keeps
the current journal unchanged and advances metadata serial; it cannot introduce,
erase, rewind or complete an intent. Changed/missing unresolved evidence remains a
conflict. Existing activity, maintenance and grant-stop requirements still apply.

`POST /v1/topologies/{name}/state/snapshots/retention-plan` accepts an admin request
such as `{"keep_last":3,"older_than":"2026-10-01T00:00:00Z","limit":32}`.
Keep-last must be at least one; batch limit is 1–128. The read-only plan binds the
full aggregate/catalog SHA256, lists oldest eligible snapshot IDs, and reports
protected entries (a compact summary for large catalogs). It preserves snapshots
with unresolved journal or unfinished workflows, committed restore references,
newest retained snapshots and snapshots newer than the cutoff.

Submit the unchanged plan to `POST .../snapshots/retention-apply`. The same
aggregate transaction recomputes eligibility and rejects changed catalogs or
edited IDs with409. A successful response lists removed snapshot metadata and
`resources_deleted=false`; no provider objects are deleted. Replan after a
successful batch or a lost-response conflict. Pending GC reservations block
retention, and state/action serials and contents remain unchanged.

<a id="http-api-provider-snapshot-capability-boundary"></a>
#### Provider snapshot capability boundary

Aggregate snapshots cover metadata (state and action journal), not VM memory,
container filesystems or provider disks. At the migration baseline `main=b1cc43d`
and current refactor, Docker, Firecracker and libvirt do **not** implement the
`driver.Snapshot` SPI; the interface alone does not advertise a usable capability.
There is therefore no existing provider snapshot operation to claim migrated.

| Provider | Metadata aggregate snapshots | VM/memory/disk snapshot via Sysbox |
|---|---|---|
| Docker | Supported by the chosen state backend | Not implemented |
| Firecracker | Supported by the chosen state backend | Not implemented |
| libvirt | Supported by the chosen state backend | Not implemented |

`TestBuiltinProvidersDoNotAdvertiseVMSnapshotFromMetadataSupport` pins this
boundary. Any future provider snapshot implementation needs separate grant,
identity, storage and restore acceptance; metadata restore never substitutes for
that acceptance or rewinds external resource generations.

<a id="http-api-gc-reference-domains"></a>
#### GC reference domains

A GC reference snapshot now carries explicit `domains` for `revision`, `artifact`,
`cache` and `placement`. Each domain is `complete` with a digest/count when
references are present, or `not_applicable` when the canonical lifecycle has no
reference in that domain. Malformed domain attributes are corrupt evidence.
Catalog collection rejects any other status before producing candidates.

This prevents an incomplete domain from being treated as an empty domain. The
cross-host catalog still needs an external producer for revision/artifact/cache
inventory; until that is supplied, that requirement remains open and GC stays
fail-closed. Snapshot/journal/state references continue to be collected in the
same aggregate transaction.

<a id="http-api-durable-external-gc-reference-sources"></a>
#### Durable external GC reference sources

Topology-scoped publication uses the existing API administrator authentication:

- `GET /v1/topologies/{topology}/gc/sources` reads source versions and manifests.
- `POST /v1/topologies/{topology}/gc/sources/{source}/begin` accepts
  `{"topology_uid":"...","expected_version":0}` for first enrollment.
  The response contains the new `version` and `updating:true`.
- `POST /v1/topologies/{topology}/gc/sources/{source}/complete` accepts the same
  topology identity, the version returned by begin, and a `manifest`.

A manifest contains `domains` with all four keys (`revision`, `artifact`,
`cache`, `placement`), each a string array; `references` with exact
`source/resource_type/driver/external_id/owner_uid/resource_uid/generation`;
and an RFC3339 `valid_until`. An empty array declares an observed empty domain.
Opaque domain values are retained evidence; resource deletion protection needs
an explicit resource reference. Unknown domains and malformed references fail.

A participating publisher must successfully begin **before** changing or using
external references, and complete only after the entire inventory is durable.
No new publication may reuse an earlier completed version. Complete supports
exact replay after a lost response. A lost begin response can be inspected with
GET; it does not permit another writer to assume ownership of that publication.
An incomplete source blocks new GC. Offline/expired inventories are retained
and block new reservations. A reservation pins its immutable directory through
signed deletion completion, so expiry during deletion does not strand recovery.
All publication writes share the reservation transaction lock.

Enrollment records its source in the current format 16; other
aggregates remain unchanged. Older writers reject the enrolled aggregate.
Current source registration is not rewound by snapshot restore; retained
snapshots also protect their source references. Metadata DELETE refuses enrolled
sources. Source deregistration and nonparticipating external writers are not
supported by this protocol. Operators must enroll every required producer;
this API does not discover arbitrary external stores or make them transactional.

Production publishers opt in with the service-level `gc_publication_source`
setting (1–64 letters, digits, `_` or `-`). Give each independently writing
API/host a stable unique ID. The service registers separate `-workspace`,
`-placement`, and `-artifacts` sources. Workspace upserts, immutable revision
materialization and legacy workspace snapshots publish before changing files;
placement writes publish before changing affinity. Host execution installs the
publisher context in artifact resolution, including local files, HTTP cache
hits/downloads, mutable-URL invalidation, and Docker OCI inspect/pull.

Contributions retain earlier revision digests, artifact digests and hashed cache
locations. An inventory is fresh for 24 hours after a successful write; an idle
source that expires blocks new GC until its publisher performs a successful
publication. This is deliberately conservative; there is no automatic stale
source removal or timer-based unknown-operation recovery.

Enable producers while old writers are stopped, then start all writers with
consistent configuration. Already enrolled topologies reject a current service
with missing configuration. Local CLI execution against an enrolled topology is
rejected; use the configured host executor. Initial enrollment is not a substitute
for stopping unconfigured/old writer processes. A source left `updating` after a
crash requires explicit investigation and completion using the existing protocol;
cold startup neither resumes its mutation nor clears its record.

The global revision registry (`POST /v1/revisions`) has no topology identity and
is retained outside this per-topology publisher protocol. There is no global
revision, shared artifact/cache deletion, or source retirement authorization in
this change. Topology metadata DELETE remains blocked while sources are enrolled.

A source manifest is bounded to 4,096 entries per domain. Publishers retain
prior entries rather than evicting evidence automatically; exceeding the limit
leaves publication incomplete and requires operator investigation. Concurrent
operations on one source may receive a version conflict and must retry after the
winning operation completes. They must not clear an updating record themselves.

A source with a complete manifest can be permanently retired with
`POST /v1/topologies/{topology}/gc/sources/{source}/retire`, sending
`topology_uid` and its `expected_version` (no manifest). The transaction refuses
in-progress publication and active GC reservations. Retirement increments the
source version, keeps the complete manifest, and rejects subsequent begin or
complete calls, including old completion replay. Repeating the same retirement
request returns the existing result.

Retired sources do not need freshness renewal: their references are immutable
and continue to protect resources and caches indefinitely. Retirement is recorded in the current lifecycle format. It is not
proof that a host process stopped, does not discard references, and does not
permit metadata deletion or shared cache deletion. A failed/incomplete publisher
cannot be retired to bypass its unresolved operation.

Quiescent topology metadata DELETE can now release **domain-only** source
references in the same authority/aggregate transaction. Every source must be
retired and complete, state must have no resources, no snapshot may remain,
no source may carry explicit external resource references, and pending GC or
unresolved actions still block deletion. Existing API activity and grant-stop
checks remain mandatory. Raw state deletion cannot release sources.

The operation resets the topology UID, rejecting publication under the old
identity. Placement cleanup does not create a new publisher. Immutable files
retained for run history remain on disk; this operation does not delete their
contents or claim provider absence independently of existing cleanup evidence.

Metadata reset now commits an exact old-topology UID/serial deletion receipt.
If the process exits after reset but before workspace cleanup, the same request
can complete cleanup without creating another topology identity. Replay requires
an untouched empty successor and stopped authority for both identities. New
state, actions, snapshots or source enrollment blocks old cleanup; an unrelated
reset clears the old receipt. The receipt is retry evidence, not execution or
provider deletion authority.

<a id="http-api-global-revision-retention-preview"></a>
#### Global revision retention preview

`POST /v1/revisions/retention-plan` accepts `keep_last` (0–10000),
`older_than` (a past RFC3339 timestamp), and `limit` (1–128). It returns
`candidates`, `retained` reasons, a source digest and
`deletion_authorized:false`. Registry, lifecycle and Run observations are separate:
this preview is not deletion authority. The SQL `retention-apply` endpoint below
revalidates the policy and references inside its deletion transaction.

The preview supports file, SQLite and PostgreSQL registries, validates revision
content digests and protects historical Run references plus current and retained
snapshot lifecycle evidence. Fixed topology selectors and legacy local state
are rejected instead of silently omitting references. Registry collection is
limited to 10000 records / 64 MiB; Run-reference collection limits count to
100000 and local history bytes to 64 MiB. Lifecycle aggregates use the existing
repository reader and its locking; its decoded aggregate size is not yet bounded
by this endpoint. Large lifecycle snapshots remain an operational limitation.
Global revision objects and historical Run evidence are never deleted here.

<a id="http-api-atomic-sql-revision-deletion"></a>
#### Atomic SQL revision deletion

`POST /v1/revisions/delete` accepts `revision` and a past RFC3339 `older_than`.
It explicitly deletes one revision, independently revalidating references; a
retention preview digest is not authorization. Unscoped shared SQLite/PostgreSQL catalogs and participating local file
registries are supported. See the file registry protocol below.

Deletion locks lifecycle/global revision/Run evidence in one transaction, rejects
historical Run, snapshot, source, other revision content, command, checkpoint or
plan references, checks age and content digest, then installs admission guards
and deletes before commit. PostgreSQL explicitly uses READ COMMITTED. New nonempty
Run revisions must exist; auxiliary and lifecycle writers reject durable retired
revision references. Republishing identical bytes does not remove the retirement
marker or silently reenable auxiliary references. Administrative DDL is trusted.
Large/unresolved lifecycle catalogs remain fail-closed under existing GC rules.

The delete operation is single-object and does not remove provider resources or
local materialized trees. Missing revisions return conflict rather than inferring
that a previous request succeeded. For keep-last policy batches use the separately validated retention-apply endpoint.


<a id="http-api-atomic-retention-policy-application"></a>
#### Atomic retention policy application

`POST /v1/revisions/retention-apply` accepts the full JSON plan returned by
`retention-plan`. It rejects altered or stale previews and empty batches.
SQLite/PostgreSQL execute 1–128 unique candidates atomically. The deletion
transaction independently recalculates the newest `keep_last` set, validates
`older_than`, and rechecks all Run/lifecycle/snapshot/auxiliary references per
candidate. Any conflict rolls back all deletions, retirement markers and guard
installation; there is no partial successful batch. The preview comparison is
an early stale-plan check, not the concurrency protection.

The response lists deleted revision IDs and `atomic:true`. Replaying an applied
preview returns conflict, and a disconnected response requires a new inspection;
it does not authorize repeating the old batch blindly. File registry execution also supports this endpoint, with an atomic retirement
index commit and recoverable physical payload cleanup.


<a id="http-api-file-and-independent-registry-retirement"></a>
#### File and independent registry retirement

For the local metadata backend, `POST /v1/revisions/delete` also retires one file
revision. The same catalog `flock` spans final reference publication and deletion.
The inventory includes enrolled consumer roots, historical Runs, lifecycle and
snapshot data, decoded JSON/base64 fields, commands, checkpoints, execution
ledgers, reset return receipts, and materialized revision directories. Missing
roots, malformed evidence, symlinks, nonregular JSON records, legacy `state.json`,
and inventory budget overflow reject deletion. The scan is conservative: unrelated
content containing the digest can retain the revision.

Deletion renames the object to `<revision>.json.retired`, fsyncs the directory,
then replaces its payload with a small durable proof. The permanent filename
fences late Run, lifecycle, checkpoint, execution-ledger and registry writes,
including a cold process. A crash between retirement and compaction may retain
project bytes; it never permits republication. An interrupted response requires
inspection, not removal of the tombstone or blind replay.

Set top-level service configuration `revision_registry` to an absolute canonical
path to enroll a local metadata root in an independent file registry. Every
consumer must see the same registry and consumer paths, with reliable `flock`
and atomic rename semantics. `consumers.json` is a monotonic consumer manifest;
`.sysbox-revision-registry.json` binds each root. Enrollment into a populated
unmanifested registry, switching an existing nonempty local registry, or loss of
an already-bound manifest rejects rather than silently dropping peers. Missing
or offline consumers block deletion. This option rejects SQL state backends.

Drain old writer binaries before enrolling or enabling retirement. Arbitrary
unregistered writers, independent databases, and remote registry federation do
not participate in this file protocol. Keep the catalog, binding, manifest and
retirement proofs in backups; never delete them to unblock a request. File batches use the retention-apply endpoint and its independent policy checks.


<a id="http-api-file-retention-batch-recovery"></a>
#### File retention batch recovery

File `retention-apply` validates all 1–128 candidates, keep-last policy and every
registered consumer reference under the catalog lock. No retirement is published
if any candidate conflicts. The batch proof is durable before an atomic index
replacement commits the entire batch; all readers and writers consult this index.
A proof left before index commit does not retire anything. Missing committed
proofs, index or required anchor block admission. Inventory holds the same lock.

Physical object removal follows the index commit. A crash or removal error can
leave retired bytes, but never authorize a late Run or expose a subset of the
batch after recovery. `POST /v1/revisions/retired-cleanup` on the local backend
reclaims only already-retired `.json` object payloads and is safely repeatable.
It returns `reclaimed_objects` and `retirement_proofs_preserved:true`; it never
removes index, anchor or batch proofs. Restore all of these together from backups.
Drain writer binaries predating this batch protocol before enabling it.

<a id="http-api-fenced-disposition-of-unknown-non-docker-gc"></a>
#### Fenced disposition of unknown non-Docker GC

`POST /v1/topologies/{topology}/gc/{reservation}/host-fences` accepts the exact
retained `grant` and an `Idempotency-Key`. Only a configured disposable fencing
host is eligible. It binds the candidate/reservation digest and retires active
authority into a hold; lease expiry is not stop evidence. Record the independent
controller receipt through the existing host-fence receipt endpoint.

After whole-execution-VM fencing, run `sysbox host-fence inspect-gc <fence-id>
--socket <supervisor-socket>` in the enrolled execution host. The supervisor
obtains canonical authorization, checks the new boot and local provider scope,
observes the exact libvirt definition or Firecracker manifest, archives the old
journal and returns a signed disposition proof. Missing/corrupt journals are
retained as explicit evidence; no provider deletion is replayed. Final authority
validation runs under the topology journal lock. Released fences cannot authorize
journal rebuilding. Firecracker unmanifested scoped directory residue rejects.

Submit that proof to
`POST /v1/topologies/{topology}/host-fences/{fence}/reconcile-gc`.
The API persists the decision, retires the old command, marks the reservation
with a disposition hash, then releases authority and the host hold. Repeating the
same proof finishes an interrupted transition; different proofs conflict. The
response always retains `operation_result:unknown`, with independently observed
`present` or `absent`. It is not a deletion-success receipt. Present targets need
fresh planning; old-boot Firecracker residual cleanup remains distinct from
ordinary deletion, which continues to reject stale process anchors.

GC disposition uses execution authority version 6 and the current lifecycle format 16.
Earlier lifecycle formats are rejected; see [storage and upgrade rules](#recovery-contract).

<a id="http-api-remote-revision-consumer-pins"></a>
#### Remote revision consumer pins

A file registry service may set `revision_consumers` to an explicit map of
consumer IDs to private token-file paths. This creates a monotonic durable
consumer catalog; unknown, uninitialized and stale consumers block deletion.
Removing a consumer from configuration does not delete its retained pins.
The service exposes authenticated `POST /v1/revision-registry/{consumer}/pins`.

A separate file-backend consumer sets `revision_remote` with `url` (the service
URL ending `/v1/revision-registry`), `consumer`, and absolute `token_file`.
HTTPS is required outside loopback. The binding is persistent and cannot be
silently switched; the incarnation is unique and a durable sequence is advanced
before sending requests. Registry sequence checks reject delayed older releases.
Every participating local publisher pins revisions remotely before publishing
bytes. A lost acknowledgement leaves conservative pins; unavailability rejects
new referenced writes. Existing cached revision bytes remain local metadata.

`POST /v1/revisions/remote-sync` performs a full bounded local reference scan under
the same local catalog lock and replaces the remote pin set. Only such a complete
scan refreshes the one-hour completeness window. Schedule this endpoint before
that window expires; otherwise retirement remains blocked. Materialized revision
directories, decoded snapshots, lifecycle and return receipts retain pins. Empty
or corrupt JSON, symlinks, old state and incomplete scans never release pins.
After retiring local evidence, sync explicitly before deleting the upstream
revision. Token files must be private regular files; redirects are rejected.

Roll out only after draining older publishers. Remote consumer mode cannot be
combined with shared-path registry binding or downstream registry service mode.
This implementation supports explicit file-backend consumers over HTTPS; SQL
consumers and arbitrary unregistered databases remain rejected, not inferred
empty. Real isolated-VM HTTPS admission/release/late-reference refusal was tested
without shared work directories.

After a `present` Firecracker disposition, a newly planned GC reservation on the
same immutable generation may reclaim old-boot residue. The supervisor reads the
manifest's original boot and asks the authenticated
`POST /v1/agents/{agent}/gc-cleanup-proof` route for matching released history.
The old proof establishes process exclusion only; the current command/grant and
fresh reference reservation authorize this new deletion. Full original manifest
bytes, proof and new grant are durably archived outside the removed directory.
No old PID is signalled. Current-boot and never-started generations use ordinary
GC. A new successful receipt belongs only to the new reservation.

<a id="http-api-explicit-ownership-transfer"></a>
#### Explicit ownership transfer

`POST /v1/topologies/{target}/ownership-transfers/prepare` accepts an immutable
`OwnershipTransferPlan`: source/target topology UIDs, resource UIDs/generations,
addresses and serials, exact external ID/provider, owning Agent and inspection
fingerprint. Both placements must use that Agent. The target is durably reserved;
the source maintenance barrier revokes grants and waits for verified stops. A
stale source plan is rejected before target reservation.

On the owning host, `sysbox host-fence prepare-transfer <plan.json> --socket ...`
asks canonical authority, verifies the stopped provider object and recovery state,
then rechecks the freeze and signs the preparation. Submit the proof to
`POST /v1/topologies/{target}/ownership-transfers/prepared`. Confirm with the same
plan at `POST /v1/topologies/{target}/ownership-transfers`. Confirmation requires
that stored host preparation, commits source retirement under authority exclusion,
and queues a normal signed host adoption command carrying the retirement proof.
Target completion consumes the host receipt, commits target state, stops the new
grant and releases the source barrier. Matching completed POSTs return the
original operation instead of reapplying.

Before source retirement, `POST .../ownership-transfers/abort` permanently marks
the target reservation aborted. A delayed preparation cannot reopen it. Release
of the source hold still waits for stop evidence and checks the exact plan;
retirement cannot be silently rolled back through abort. After retirement, any
external administrator modification that invalidates provider evidence retains
the hold for explicit reconciliation.

Docker uses a signed durable transfer sidecar because its original labels are
immutable. Policy, GC, lifecycle, reset-old-object and discovery consult effective
ownership; missing required bindings fail closed. Successive transfers preserve
an atomic signature chain. Firecracker requires a stopped VMM with no unresolved
launch/attachment; the original artifacts stay in place and signed manifest
history validates the new ownership at the original physical path. libvirt
requires a stopped persistent definition and exact metadata readback; storage
and networking remain external and are not deleted as source-owned disk paths.
Transfer reservations use the current lifecycle format 16; do not mix incompatible writers.

<a id="http-api-independent-sql-consumer-retirement"></a>
#### Independent SQL consumer retirement

A file-registry coordinator can configure `revision_registry_id` and
`revision_sql_consumers` (consumer name → `url`, `token_file`). Each SQLite or
PostgreSQL consumer configures `revision_upstreams` (registry ID → private token
file). The consumer endpoint `POST /v1/revisions/federated-retire` accepts the
registry/request ID, exact revision batch and cutoff; it revalidates its entire
enrolled catalog and commits retirement guards and an idempotent acknowledgment
in the same local SQL transaction. Unknown/unenrolled catalogs are rejected.

The coordinator keeps a durable consumer manifest and a parent enrollment marker;
removing a configured peer or losing/truncating that manifest blocks deletion.
It persists the retirement intent and holds its local publication lock while
collecting every consumer acknowledgment. Only then does it commit its own atomic
retirement. A missing local SQL object still requires reference validation and
permanent retirement fencing before acknowledgment. Batch conflicts roll back
all local SQL changes. HTTP retries verify the same request and revision list;
HTTPS is required outside loopback and redirects are refused.

This is conservative distributed retirement, not a cross-database rollback
transaction. If some consumers retire and another is offline, the central object
remains and the completed consumers stay fenced. Retry the same request after
resolving the blocker; never remove their tombstones or drop the offline peer.
Existing required Run/auxiliary writer guards remain active. All participating
repositories must be explicitly enrolled before retirement is enabled.


新增服务接口：POST /v1/topologies/{topology}/plan 接受 revision、inputs、target，返回 summary/actions；不发布 workspace 指针。Apply 接受 target；Reset 和 POST /v1/runs/{id}/resume 支持 Idempotency-Key。POST /v1/runs/{id}/cancel 只请求取消，不修改正式完成状态。

Console 已统一到宿主受控 guest task，使用普通用户、签名期限和 cgroup 停止证明。已删除原生 Console SPI/transport 与 Agent 直连路径；没有 helper/隔离条件时拒绝开启。断连和取消请求仍不等于进程已停止。

### 组件构建身份核对

`GET /v1/health` 返回 `build`（version、commit、build_time、go_version）和 `agent_protocol`；启用 API 认证时，已注册 Agent 可使用自己的签名凭据读取，无需管理员 token。
宿主 socket 的 `version` 操作返回 `build_version`、`build_commit`，仍要求协议版本 1 和空资源 target。
Agent 启动前核对自身、宿主和 API 的版本及完整 40 位 commit，同时核对 API Agent 协议。
缺失、不同版本或不同 commit 都拒绝启动；启动时核对不是制品签名，也不代替原有授权校验。
三方均为 `dev@unknown` 的源码开发构建可运行，但会明确提示未验证发布身份，不能作为发布配套验收证据。
此检查发生在 Agent 启动阶段；运行中单独升级 API/宿主不属于支持的升级流程，应停止旧服务后成套部署并重新启动 Agent。

旧的 `POST /v1/runs/{id}/recover`、`/cleanup` 返回 410，不读取或修改资源。恢复使用已有 owning-host observe/recover 协议，回收使用授权 Destroy；无宿主归属的旧记录须离线迁移。

### Run 结果与回收契约（协议 1）

`GET /v1/capabilities` 的 `run_error_protocol=1` 表示 Run 可返回 `error_code`；
`cleanup_receipt_protocol=1` 表示整拓扑 Destroy 的宿主成功提交必须携带有效 `cleanup`。
定向 Destroy 不提供整拓扑清理证明，不能据此释放 topology。

`error_code` 是诊断分类，不是资源不存在或任务已停止的证明。宿主按结构化错误原因分类：
`observation_unknown`、`action_uncertain`、`plan_stale`、`state_missing`、`state_conflict`、
`unsupported`、`deadline_exceeded`、`cancelled`，其余为 `operation_failed`；控制面租约过期使用 `lease_expired`。
缺失或未来未知 code 应按未知失败处理；不得解析 `error` 文本决定回收，也不得仅凭 code 自动释放资源。
历史记录或尚无结构化错误的停止路径可能不带 code。成功结果不携带 error/error_code。

消费者应将 Run 的 topology、操作、幂等键与提交请求对应。整拓扑 Destroy 的 `done` 还需核对
cleanup 的 version=1、empty=true、state_serial>0，以及 RunID、topology、AgentID、queued_at、revision、attempt、lease_owner 绑定。
回执证明该次宿主提交时的受管资源回收结果，不证明此后没有新操作。
继续读取 cleanup-inspection，要求没有资源、未完成动作或活动操作，且 `cleanup_receipt_status=current_recorded`，
再调用普通元数据 DELETE。API 在删除时重新核验最新归属与回执；查询快照不能替代此原子校验。
禁止 force 删除作为失败回收 fallback。响应丢失后重试原操作键，不能把单独 404 或空列表当作已回收。


### 投递确认与超时停止证据

`acked`/`running` 的命令投递租约过期后，仅当同一 Run 仍 assigned、没有取得 Run claim、且授权 ledger 中没有该命令/动作的现行或历史执行授权时，才允许在原记录上恢复 queued 投递。新的投递递增 attempt，旧 attempt 不能更新新记录。已取得执行权或结果未知的任务不通过此路径重放。

API 收敛超时使用 `error_code=convergence_deadline_exceeded`，与宿主 context 超时的 `deadline_exceeded` 分开。该业务失败不会被后来宿主成功覆盖；同一执行身份的有效签名宿主停止证据仍可落库、清理 Run lease 和释放执行授权。宿主实际结果保留在 HostCompletion 证据中，重试要求完全相同的签名证据。其他终态与不同 attempt 不被放宽。

### Windows 离线镜像交付

镜像构建与资源 Apply 分开：现有 `scripts/windows/build-windows10.sh` 等入口只在专用构建宿主显式运行；
运行时仅消费已核验的 qcow2，不接受 `windows_cache.build_script` 或由 HCL 触发宿主镜像构建。
从 #79 提取的封存检查位于 `scripts/windows/inspect-artifact.py`，在构建、generalize/seal、关机完成后执行：

```sh
python3 scripts/windows/inspect-artifact.py /srv/images/windows-base.qcow2 /srv/images/windows-base.json --system-partition /dev/sda1
```

输入必须是只读、独立无 backing chain 或 external data-file 的 qcow2；显式指定 Windows 系统分区。
脚本仅调用 `qemu-img info` 和只读 `guestfish`，拒绝非封存状态、查询失败或检查期间变化的文件，生成不可覆盖的 SHA-256、大小与封存状态报告。
它不修改或启动镜像，不证明 QGA、网络、任务 helper 已通过运行验收；报告的 `runtime_accepted` 固定为 false。
既有构建器不会被此检查自动执行，也不假定其产物已经 generalize/seal。
报告中的 source/sha256 可用于既有 `sysbox_image` 字段；报告 size 是文件字节数，不自动映射 HCL size，源文件须通过宿主配置允许的制品路径提供。
不引入 WinRM 管理层、业务域/账号配置或外部 endpoint 管理器；实际 Windows 版本及 helper 配套仍需单独验收。
