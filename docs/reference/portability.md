# Artifact 与 Topology 可移植性规范

本文定义**多个独立环境之间可交叉部署**所需遵守的约定。目标读者是各自拥有
独立镜像构建与 topology 仓库的环境（例如 `sysfield`、`cyberfield`）：它们
彼此不共享代码、registry 或缓存，但只要双方都遵守本规范，一方跑通的环境就能
原样部署到另一方，无需修改 topology 或重新构建镜像。

这是「对齐基准」，不是操作手册。字段语义见 [HCL Reference](hcl.md)，准备与
缓存细节见 [Artifacts](../operations/artifacts.md)，写法指南见
[Authoring Topologies](../guides/authoring-topologies.md)。本页只规定
**对齐所需的那部分约定**，其他文档是唯一事实来源，本页不复制它们。

## 1. 镜像命名

本地构建的 OCI 镜像必须带**所有者前缀**，避免跨环境在共享 Docker daemon 上
发生命名冲突。格式：

```
<owner>-<role>:<version>
```

- `<owner>`：环境标识（`sysfield`、`cyberfield` 等），小写，短横线分隔。
- `<role>`：节点角色，表达用途而非临时实现（`web-target`，不是 `ubuntu_1`）。
- `<version>`：语义化版本（`0.1.0`）。

示例：

```
sysfield-web-target:0.1.0
sysfield-enterprise-node:0.1.0
cyberfield-sql-injection:2.3.0
```

- 长期身份（研究复现、生产部署）用 registry digest 而非可变 tag。
- 上游公共镜像（`ubuntu:24.04`、`alpine:3.22`、`python:3.12-alpine`）无需
  前缀，但应固定到具体 tag，不用 `latest`。
- 镜像的构建脚本（Dockerfile、`build.sh`）归**镜像所有者**维护，不进 sysbox
  仓库；topology 只引用镜像名，不关心它是谁、怎么构建的。

## 2. Artifact 引用

每个 artifact 的 `source` 形式由 `kind` 决定，跨环境必须一致：

| kind | substrate | source 形式 | 环境注入 |
|---|---|---|---|
| `oci` | docker | `<owner>-<role>:<version>` 或 registry digest | 通常不注入 |
| `rootfs` | firecracker | 本地 ext4 路径 | `env_optional("SYSBOX_ROOTFS")` |
| `kernel` | firecracker | URL + `sha256` | 不注入（content-addressed） |
| `qcow2` | libvirt | 本地 qcow2 路径 | `env("SYSBOX_QCOW2")` 或等价 |

关键约束：

- **kernel 必须带 `sha256`**，`source` 用稳定 URL；sysbox 下载后 content-addressed
  缓存，两个环境引用同一个 URL 就得到同一份 kernel，无需各自准备。
- **rootfs 路径不得硬编码进 topology**。用 `env_optional("SYSBOX_ROOTFS")` 覆盖，
  否则回退到 `$HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4`（与
  `scripts/prepare-fc-rootfs.sh` 的默认输出一致）。
- **qcow2 路径不得硬编码**，用 `env()` 注入。
- 所有 artifact 的 `architecture`（如 `amd64`）与 `guest_family`（如 `linux`）
  必须显式声明。

## 3. 目录布局

环境可移植的前提是 artifact 落在约定的标准位置，而不是各环境自创目录：

```
$CACHE/tools/firecracker            # firecracker 二进制（SYSBOX_PROVIDER_FIRECRACKER_BIN）
$CACHE/rootfs/ubuntu-24.04.ext4     # firecracker rootfs（SYSBOX_ROOTFS）
$CACHE/artifacts/<sha>/<name>       # kernel 等下载缓存（sysbox 自管，勿手写）
$HOME/firecracker/<vm-id>/          # firecracker per-VM 可变状态（workdir）
$HOME/libvirt/<domain>/             # libvirt per-VM overlay（workdir）
```

- `$CACHE` 默认 `/var/cache/sysbox`（部署）或 `~/.cache/sysbox`（本地 CLI），
  由 sysbox 的 `paths.cache` 决定，环境通过 config/env 对齐，不在 topology 里假设。
- **topology 从不直接引用 `$HOME/...` 下的 per-VM 目录**——那些是运行时生成物，
  不是 artifact。
- 可变状态与只读 artifact 分离：artifact 在 `$CACHE`，per-VM 状态在 `$HOME`。
  这保证环境可以安全地重建可变目录而不丢失 artifact，也可以共享 artifact 缓存。

## 4. Topology 写法

可交叉部署的 topology 必须把「环境相关」与「场景本质」分开：

- **场景本质**写死在 HCL：substrate 别名、节点角色、拓扑结构、网络划分、
  攻击面语义、provisioner。这部分换环境也不变。
- **环境相关**通过 `env_optional()` / `env()` 注入：rootfs/qcow2 路径、
  网络 octet（避免多环境地址冲突）、SSH 授权 key、apt 镜像源。示例：

  ```hcl
  locals {
    rootfs_path    = env_optional("SYSBOX_ROOTFS") != "" ? env_optional("SYSBOX_ROOTFS") : "${env_optional("HOME")}/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
    network_octet  = env_optional("SYSFIELD_NETWORK_OCTET") != "" ? env_optional("SYSFIELD_NETWORK_OCTET") : "78"
    ssh_authorized = env_optional("SYSFIELD_SSH_AUTHORIZED_KEYS") != "" ? [env_optional("SYSFIELD_SSH_AUTHORIZED_KEYS")] : []
  }
  ```

- 资源 label 用稳定角色名（`sysbox_node.attacker`、`sysbox_network.experiment`），
  不用临时实现名。
- 引用自动形成依赖；只在引用无法表达时用 `depends_on`。
- provider 专属参数（`privileged`、`binds`、`allow_direct` 等）留在 provider 块。

## 5. 交叉部署验收

一个环境声称「符合本规范」前，应通过：

1. **可复现**：从干净 checkout 只跑 `make prepare-*`（如有）+ `sysbox apply`，
   plan 首次后 no-op。
2. **不硬编码**：`grep` topology 无环境专属 IP 前缀、无 `$HOME`/`/home/` 绝对
  路径、无明文凭证；环境相关项一律走 env 注入。
3. **可互换**：把 A 环境的 topology + 镜像名交给 B 环境，B 只改自己的 env
   （`SYSBOX_ROOTFS`、octet、key），不改 topology，即可 apply 成功。

三个条件同时满足，环境才算对齐；任一失败都说明还有隐含耦合。
