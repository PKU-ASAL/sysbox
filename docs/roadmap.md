# sysbox 开发路线图

> 版本：v0.2 · 2026-05-18

---

## 0. 定位与边界

**sysbox 做什么**：多 substrate（container / microVM / VM）的 Terraform——用 HCL 声明拓扑，plan / apply / destroy，加一层基础运维 API。

**sysbox 不做什么**：EDR 事件采集。在节点里部署 sensor / EDR agent 走普通 provisioner 路径（`provisioner "exec"` / `provisioner "file"`），和安装任何其他软件没有区别。EDR 的事件上报、存储、分析由 EDR 自身负责，sysbox 不介入。

```
┌──────────────────────────────────────────────────┐
│  IaC 核心                                         │
│  plan / apply / destroy                           │
│  substrate: container / microVM / VM              │
├──────────────────────────────────────────────────┤
│  运维 API  (sysbox serve)                         │
│  拓扑生命周期 · 节点 exec / file / status         │
├──────────────────────────────────────────────────┤
│  Provisioner                                      │
│  exec · file  ← 包括安装 EDR agent               │
└──────────────────────────────────────────────────┘
          ↑ sysbox 边界
EDR 事件采集 / 存储 / 分析  ← EDR 自己的事
```

---

## 1. 当前状态（Wave 1 已完成）

| 模块 | 状态 | 备注 |
|---|---|---|
| Substrate 接口（19 方法） | ✅ | |
| BaseSubstrate（9 个默认实现） | ✅ | |
| Capabilities（11 字段，typed） | ✅ | |
| NodeHandle / NodeSpec / ConnInfo / LinkRequest | ✅ | 全部 typed struct |
| Docker provider | ✅ | 全接口覆盖，编译期 guard |
| Firecracker provider | ✅ | 全接口覆盖，编译期 guard |
| Runtime 零 substrate 硬编码 | ✅ | 无 `if subName ==`，无具体类型断言 |
| State schema v2 | ✅ | v1 直接报错，不留兼容层 |
| HCL schema | ✅ | `provider "X" {}`、`connection {}`、`for_each`、`locals`、`output` |
| CLI（9 个命令） | ✅ | init/plan/apply/destroy/state/show/output/validate/sensor |
| 4 个 examples + lab.sh | ✅ | `make lab SUITE=xxx` 全部验证通过 |
| Makefile（SUITE 参数化） | ✅ | |

未完成：

| 模块 | 状态 |
|---|---|
| 运维 API（`sysbox serve`） | ❌ |
| `count` 元参数 | ❌ |
| `for_each` 完整化 | ⚠️ 骨架已有，边界情况未覆盖 |
| `module` 块 | ❌ |
| libvirt substrate | ❌ |
| ImageSpec union（qcow2 / ISO / cloudinit） | ❌ |

---

## 2. Terraform 对齐差距

| 差距 | 优先级 | 说明 |
|---|---|---|
| `count` 元参数 | **P1** | 最常用的多实例语法，1–2 天可完成 |
| `for_each` 完整化 | **P1** | 已有骨架，补全 set 类型和边界情况 |
| `module` 块 | **P2** | 复用拓扑片段；Wave 3 |
| `data` source | **P2** | 查询已有网络 / VM；Wave 3 |
| `import` 命令 | **P3** | 把已有容器/VM 纳入 state |
| `lifecycle` 块 | **P3** | prevent_destroy / ignore_changes |
| remote state | **P4** | 当前单机足够 |
| workspace 命名空间 | **P4** | SUITE= 参数已覆盖核心需求 |

---

## 3. Wave 2 · 运维 API + Terraform P1 差距（~10 天）

### PR-07 · `sysbox serve` + 拓扑管理 API（4 天）

新增 `pkg/api/` 和 `cmd/sysbox/commands/serve_cmd.go`。

**路由**：

```
GET  /v1/health

GET  /v1/topologies                          扫描 runs/*/state.json，列出所有 suite
GET  /v1/topologies/{suite}/state            完整 state JSON
GET  /v1/topologies/{suite}/plan             计算 plan（只读，不执行）
POST /v1/topologies/{suite}/apply            触发异步 apply → {run_id}
POST /v1/topologies/{suite}/destroy          触发异步 destroy → {run_id}

GET  /v1/runs/{run_id}                       run 状态 + summary
GET  /v1/runs/{run_id}/logs                  SSE 流：apply/destroy 实时日志行
```

apply / destroy 是阻塞操作，包进 goroutine + in-memory job store（`map[string]*Run`）。
日志行写入 broadcast buffer，SSE 客户端订阅；apply 完成后 channel 关闭，SSE 连接自然断开。

认证：`SYSBOX_API_TOKEN` 环境变量。设置时要求 `Authorization: Bearer <token>`；未设置则无认证（dev 模式）。

包结构：

```
pkg/api/
  server.go        http.Server + middleware（auth / logging）
  routes.go        chi 路由注册
  handler_topo.go  plan / apply / destroy handlers
  jobs.go          Run store（in-memory）
  sse.go           SSE broadcast buffer
```

### PR-08 · 节点访问 API（3 天）

```
GET  /v1/topologies/{suite}/nodes               从 state 列出节点
GET  /v1/topologies/{suite}/nodes/{name}        节点详情（handle + status）
POST /v1/topologies/{suite}/nodes/{name}/exec   执行命令，chunked streaming stdout/stderr
POST /v1/topologies/{suite}/nodes/{name}/files  上传文件（multipart）
```

exec 实现：从 state 重建 `NodeHandle` → `substrate.Get(provider).Connection(handle).ExecInline(cmd, w, w)`，stdout/stderr 直接写 `http.ResponseWriter`（chunked transfer）。

### PR-09 · `count` + `for_each` 完整化（3 天）

```hcl
resource "sysbox_node" "attacker" {
  count = 3
  image = sysbox_image.alpine.id
  # count.index 可在表达式中引用
}
```

- `count` 元参数注入 `count.index` 到 eval context，展开为 N 个独立资源（`attacker[0]` / `attacker[1]` / `attacker[2]`）
- `for_each` 补全 set 类型支持 + 资源引用 `each.key` / `each.value`

### Wave 2 汇总

| PR | 内容 | 估时 | 依赖 |
|---|---|---|---|
| PR-07 | `sysbox serve` + 拓扑管理 API | 4 天 | — |
| PR-08 | 节点访问 API（exec / file） | 3 天 | PR-07 |
| PR-09 | `count` + `for_each` 完整化 | 3 天 | — |

**Wave 2 总人天：~10 天**。PR-07/08 串行，PR-09 可并行。

---

## 4. Wave 3 · VM substrate + Terraform P2（~18 天）

### PR-10 · libvirt substrate（7 天）

`pkg/provider/libvirt/`：

- domain XML 生成（libvirt Go SDK）
- cloudinit NoCloud 镜像注入
- `CreateNode / StartNode / StopNode / DestroyNode / NodeStatus`
- `Connection`：SSH（同 FC 路径）+ serial console
- `AttachNIC`：virsh attach-device，支持 bridge / macvtap
- 编译期接口 guard
- 新 example `examples/libvirt-vm/`

### PR-11 · ImageSpec union（3 天）

```go
type ImageSpec struct {
    Kind      ImageKind      // docker | rootfs-ext4 | qcow2 | iso
    Source    string         // URL 或本地路径
    SHA256    string
    Size      string
    Cloudinit *CloudinitSeed
}
```

artifact resolver 扩展：qcow2 下载 + sha256 验证 + 本地缓存（与现有 kernel artifact 路径一致）。

### PR-12 · `module` 块（5 天）

```hcl
module "lab_net" {
  source        = "./modules/three-tier-net"
  cidr_dmz      = "10.0.1.0/24"
  cidr_internal = "10.0.2.0/24"
}

resource "sysbox_node" "attacker" {
  link { network = module.lab_net.dmz_id }
}
```

递归解析 HCL + 模块变量传递 + 模块内 output 引用。

### PR-13 · 三 substrate e2e（3 天）

`tests/e2e/multi_substrate_test.go`：docker + firecracker + libvirt 各一节点，apply / plan / destroy 全流程。

### Wave 3 汇总

| PR | 内容 | 估时 | 依赖 |
|---|---|---|---|
| PR-10 | libvirt substrate | 7 天 | PR-11 |
| PR-11 | ImageSpec union | 3 天 | — |
| PR-12 | module 块 | 5 天 | — |
| PR-13 | 三 substrate e2e | 3 天 | PR-10 |

**Wave 3 总人天：~18 天**。PR-11/12 可并行，PR-10 依赖 PR-11，PR-13 依赖 PR-10。

---

## 5. Wave 4 · 远期 backlog（不排期）

| 功能 | 触发条件 |
|---|---|
| `data` source（查询已有 Docker 网络/VM） | 出现具体需求 |
| `import` 命令（把已有节点纳入 state） | 出现迁移场景 |
| `lifecycle` 块（prevent_destroy 等） | 多人协作或 CI 保护 |
| remote state（S3 / HTTP backend） | 多机部署需求 |
| Pause / Resume（快速重置靶场） | 高频重置场景 |
| Windows substrate（WinRM + sysprep） | Windows 靶场需求 |

---

## 6. 里程碑

```
Wave 1  已完成  ─────────────────────────────────  M1: 双 substrate + 接口稳定
Wave 2  ~10天   PR-07/08 ────────  PR-09 ────────  M2: 运维 API 可用；count/for_each
Wave 3  ~18天   PR-11 ──  PR-10 ──────────  PR-13  M3: libvirt + module + 三 substrate e2e
                PR-12 ──────────────────────┘
```

按 1 人 60% allocation：M2 约 3 周，M3 约 8 周（M2 完成后继续）。

---

## 7. 关于 EDR / sensor 部署

不需要在 sysbox 里做特殊抽象。用标准 provisioner 路径即可：

```hcl
resource "sysbox_node" "target" {
  image     = sysbox_image.ubuntu.id
  substrate = substrate.docker.dk

  provisioner "file" {
    source      = "/local/path/to/agent.deb"
    destination = "/tmp/agent.deb"
  }

  provisioner "exec" {
    inline = [
      "dpkg -i /tmp/agent.deb",
      "systemctl enable --now my-edr-agent",
    ]
  }
}
```

`Connection().CopyFile` 和 `Connection().ExecInline` 在所有 substrate 上已统一，三种节点类型（container / microVM / VM）走同一套代码路径。EDR 的事件上报地址、采集配置、存储均由 EDR 侧管理，sysbox 不介入。
