# sysbox v1.0 — Breaking Changes

> sysbox v1.0 是一次系统性收口升级，**不向后兼容** v0.x。本文件汇总所有破坏点 + 用户迁移操作。
>
> 设计依据：[`docs/architecture-review-multi-substrate.md`](docs/architecture-review-multi-substrate.md)
> 执行计划：[`docs/refactor-plan-multi-substrate.md`](docs/refactor-plan-multi-substrate.md)

---

## 0. 升级三步法

```bash
# 1. 销毁所有 v0.x lab
sysbox destroy --all   # 或手动 rm -rf runs/

# 2. 按本文件 §1-§3 改写 .hcl 文件
$EDITOR examples/three-nodes/topology.hcl

# 3. 用 v1.0 重新 apply
sysbox apply
```

---

## 1. HCL 破坏（按 resource 类型分）

### 1.1 `sysbox_node` —— 字段全部按职责分块

**v0.x（删除）**
```hcl
resource "sysbox_node" "web" {
  substrate     = substrate.docker.light
  image         = sysbox_image.web.id

  vcpus         = 2                     # 通用资源
  memory        = "512"

  privileged    = true                  # docker-only
  pid_mode      = "host"                # docker-only
  cgroupns_mode = "host"                # docker-only
  binds         = ["/var/run:/var/run"] # docker-only

  kernel        = sysbox_kernel.fc.id   # firecracker-only
  rootfs        = "/path/rootfs.ext4"   # firecracker-only
  chain_init    = "/sbin/init"          # firecracker-only

  ssh_user      = "root"                # vm 视角
  ssh_pass      = "root"
  ssh_port      = 22

  env           = { FOO = "bar" }
}
```

**v1.0（新）**
```hcl
resource "sysbox_node" "web" {
  substrate = substrate.docker.light
  image     = sysbox_image.web.id

  # 通用资源（每个 substrate 都尊重；docker 转成 --cpus/--memory，FC 转成 vCPU/MB）
  vcpus  = 2
  memory = "512"

  env = { FOO = "bar" }

  # 一个 node 选一种 substrate provider 块（label 必须等于 substrate 类型）：
  provider "docker" {
    privileged    = true
    pid_mode      = "host"
    cgroupns_mode = "host"
    binds         = ["/var/run:/var/run"]
  }
  # 或：
  provider "firecracker" {
    kernel     = sysbox_kernel.fc.id   # sysbox_kernel.X.id 引用，会自动建图依赖
    rootfs     = "/path/rootfs.ext4"   # 可选，覆盖 image
    chain_init = "/sbin/init"
    ssh_user   = "root"                # SSH 回退（rootfs 无 sysbox-init 时）
    ssh_pass   = "root"
    ssh_port   = 22
  }

  connection {
    type = "auto"                       # auto | docker | ssh | vsock
    user = "root"                       # ssh 用
    # password 字段保留但仅限测试；生产用 private_key
  }
}
```

> **PR-03 实现说明**：`provider "X" {}` 块由对应 substrate 的 `DecodeProviderConfig(body, ctx)` 自行解码，runtime 不再硬编码字段名。新增 substrate（libvirt、kubevirt）只需在自己包里写 `Config` struct + `DecodeProviderConfig` + `Dependencies` 三个方法，不用碰 `pkg/config/schema.go`。

### 1.2 `sysbox_monitor` —— 从 syscall 解析器变 EDR 编排器

**v0.x（删除）**
```hcl
resource "sysbox_monitor" "lab" {
  backend = "tracee"
  nodes   = [sysbox_node.node_attack.id, sysbox_node.node_web.id]
  events  = ["execve", "openat", "connect"]
  extra   = { sensor_container = "sysbox-sensor", tracee_bin = "/tracee/tracee" }
}
```

**v1.0（新）**
```hcl
resource "sysbox_monitor" "lab" {
  backend = "edr-falcon"                # 或其他注册的 EDR backend
  nodes   = [sysbox_node.node_attack.id, sysbox_node.node_web.id]

  agent {
    binary  = "https://releases.example.com/falcon-agent-1.2.3.tgz"
    sha256  = "..."
    tags    = { episode_id = "ep-001", role = "attacker" }
  }

  collector {
    addr = "http://host.docker.internal:9100/v1/events"
    # 如未指定，默认连本机 sysbox-collector
  }
}
```

**事件路径不变**：仍然落到 `runs/<run_id>/events/<node_id>.jsonl`。

### 1.3 `sysbox_image` —— 支持 union kind

**v0.x（兼容）**：原 `docker_ref` / `rootfs` / `sha256` / `size` 字段保留，但语义改为 image kind 自动推断。

**v1.0 新增**：
```hcl
resource "sysbox_image" "ubuntu_vm" {
  substrate = substrate.libvirt.default

  kind   = "qcow2"
  source = "https://cloud-images.ubuntu.com/.../ubuntu-24.04-cloudimg-amd64.img"
  sha256 = "..."
  size   = "20Gi"

  cloudinit {
    user_data    = file("seed/user-data")
    meta_data    = file("seed/meta-data")
    network_data = file("seed/network-config")
  }
}
```

`kind` 取值：`docker` | `rootfs-ext4` | `qcow2` | `iso`。

### 1.4 其他资源

| 资源 | v0.x → v1.0 |
|---|---|
| `sysbox_network` | 无破坏 |
| `sysbox_kernel` | 无破坏 |
| `sysbox_router` | 无破坏 |
| `sysbox_firewall` | 无破坏 |
| `sysbox_ssh_access` | 无破坏（语义保持） |
| `sysbox_actor` | 无破坏 |

---

## 2. State 文件破坏

`runs/*/state.json` schema 完全更换：

- 旧版的 `instance.container_id / instance.nics[].kind / instance.vsock_uds / instance.vm_dir` 等扁平字段全部移除
- 新版结构：`instance = { id, net{...}, conn{...}, provider{...} }`
- **不提供自动迁移**。用户必须 `sysbox destroy --legacy` 或 `rm -rf runs/<run_id>` 后用 v1.0 重新 apply
- 旧 state 可用 `sysbox state inspect <path>` 只读查看（仅 debug 用途）

---

## 3. Go API 破坏（影响嵌入 sysbox 作为库的用户）

| 删除 | 替代 |
|---|---|
| `substrate.Substrate.ExecInNode` | `Substrate.Connection(handle, hint).ExecInline` |
| `substrate.Substrate.CopyToNode` | `Connection.CopyFile` |
| `substrate.Substrate.CopyFromNode` | （v1.0 不提供；调用 `Connection.ExecInline("tar c ...")` 取代） |
| `substrate.Substrate.AttachTTY` | `Substrate.Console(handle, kind)` |
| `substrate.DockerCapable` interface | 走标准 `Substrate.AttachNIC` + `LinkRequest` |
| `substrate.NodeHandle.Attributes map[string]any` | typed `NodeHandle{ ID, Net, Conn, Provider }` |
| `monitor.Target.Substrate string` | `monitor.Backend.Supports(Target)` 路由 |
| `monitor.Backend.Start / Stop` | `Backend.Deploy / Collect / Remove` |

---

## 4. CLI 破坏

| 命令 | 变化 |
|---|---|
| `sysbox sensor start` | **删除**。Monitor 现在是 `sysbox_monitor` 资源的一部分，`apply` 时自动 Deploy + Collect |
| `sysbox sensor stop` | **删除**。`destroy` 时自动 Remove |
| `sysbox state inspect` | **新增**。只读查看 v0.x 或 v1.0 state |
| `sysbox apply / destroy / plan / output` | 保持，参数兼容 |

---

## 5. Capabilities 接口扩字段（影响 substrate 实现者）

`substrate.Capabilities` 从 4 字段扩到 11 字段（详见接口定义）。所有 substrate 实现必须填新字段，否则编译失败。

`pkg/substrate/base.go` 提供 `BaseSubstrate{}`，嵌入即可获得默认值。

---

## 6. 迁移检查清单

升级前自查：

- [ ] `git tag v0.x` 保留旧版本
- [ ] 所有 lab 已 `destroy`，`runs/` 已清空
- [ ] `.hcl` 文件已按 §1 改写（重点：`provider {}` 块、`resources {}` 块、`connection {}` 块）
- [ ] 若是 sysbox library 用户：已按 §3 更新调用代码
- [ ] CI 脚本里 `sysbox sensor start` 已删除（§4）

---

*v1.0 发布日期：TBD · 本文件随重构 PR 逐步完善*
