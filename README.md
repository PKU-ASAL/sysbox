# sysbox

> AI 红队的 Terraform —— 一键搭起 Linux 攻防战场，实时采集 eBPF 行为轨迹。

## 概览

sysbox 是一个面向 AI 红队研究的实验室编排工具。用 HCL 描述攻防拓扑，底层通过 Docker + linux-bridge 搭建隔离节点网络；AI agent（via opencode）在攻击节点内执行红队任务，tracee eBPF 传感器实时捕获各节点的系统调用行为，matcher 将事件按 PID 树归因到 agent，输出结构化的 episode 报告。

```
HCL topology
  └─ sysbox apply       → Docker containers + linux-bridge networks
  └─ sysbox sensor start→ tracee (mntns-scoped) → per-node events/*.jsonl
  └─ opencode agent     → red-team episode (tool calls via SSE)
  └─ sysbox match run   → PID-tree attribution → episode_report.json
```

## 要求

- Linux kernel（netns 支持，6.x 推荐）
- Docker daemon + sysbox-runc（for privileged inner containers）
- Go 1.22+
- `apply` / `destroy` / `sensor` 需要 root（netlink + eBPF）

## 快速开始

```bash
make build          # 编译 bin/sysbox
make lab-up         # 搭建三节点实验室 + 启动 eBPF 传感器
make lab-status     # 查看节点、状态、传感器
make test-e2e       # 运行 e2e 测试（无需 lab）
make lab-down       # 销毁实验室
```

## 目录结构

```
sysbox/
├── bin/                        # 编译产物（gitignore）
├── cmd/
│   └── sysbox/                 # 主 CLI（apply/plan/sensor/match/state/output）
├── examples/
│   └── three-nodes/            # 三节点攻防实验室
│       ├── field.sysbox.hcl    # 拓扑声明（node_attack/node_web/node_db + sysbox_monitor）
│       ├── lab.sh              # 实验室生命周期脚本
│       ├── run_opencode.py     # episode runner（SSE + opencode）
│       └── test_e2e.sh         # shell e2e 测试
├── pkg/
│   ├── config/                 # HCL 解析 + schema
│   ├── graph/                  # 资源依赖图
│   ├── hook/                   # apply/destroy 钩子
│   ├── matcher/                # PID 树构建 + 事件归因
│   ├── monitor/                # eBPF 监控 Backend 接口 + TraceeBackend
│   ├── provider/               # Docker / exec / network 底层实现
│   ├── runtime/                # apply/destroy executor
│   ├── sensor/                 # Event schema + tracee JSON 解析
│   ├── session/                # opencode session 管理
│   ├── sink/                   # JSONLSink + RoutingSink（per-node 文件路由）
│   ├── state/                  # 状态文件（原子写 + 文件锁）
│   └── substrate/              # 底层抽象注册表
├── runner/                     # Python episode runner 辅助模块
├── tests/
│   └── e2e/                    # Go 集成测试（build tag: e2e）
└── Makefile
```

## HCL 资源类型

| 资源 | 说明 |
|---|---|
| `sysbox_image` | Docker 镜像定义（Dockerfile inline 或 pull）|
| `sysbox_network` | linux-bridge 网络，带 CIDR |
| `sysbox_node` | Docker 容器节点，挂载网络，配置 env/provision |
| `sysbox_agent` | opencode AI agent，绑定节点和 API 密钥 |
| `sysbox_monitor` | eBPF 监控声明，指定监控节点列表和事件集 |

## Make targets

```
make build                  编译 bin/sysbox
make test                   单元测试（无需 Docker）
make test-e2e               Go 拓扑集成测试：apply/route/drift/destroy（需要 Docker + root）
make test-scenario          完整链路场景测试：scripted attack + agent + 归因（需要 lab + API key）
make test-scenario-no-agent 完整链路场景测试，跳过 agent（需要 lab，无需 API key）
make lint                   fmt + vet
make lab-up                 搭建实验室 + 启动传感器
make lab-down               销毁实验室
make lab-sensor-restart     传感器重启（节点重建后重新解析 mntns）
make lab-logs               tail 传感器日志
make lab-status             查看容器 / state / 传感器状态
make clean                  删除编译产物
make clean-runs             清理 episode 产物（保留 state 和 SSH 密钥）
```

## 运行一次 Episode

```bash
# 1. 确保 lab 已启动
make lab-up

# 2. 配置环境变量（.env 或 export）
export ANTHROPIC_API_KEY=...
export ANTHROPIC_BASE_URL=...
export SYSBOX_MODEL=deepseek/deepseek-r1

# 3. 运行 episode
uv run python3 examples/three-nodes/run_opencode.py

# 4. 查看报告
cat runs/default/episode_report.json

# 5. 手动匹配（可选）
make lab-status          # 获取 agent 的 anchor PID
./bin/sysbox --state runs/default/state.json \
             --file examples/three-nodes/field.sysbox.hcl \
             match run --agent red
```

## 测试

```bash
make test           # 全部单元测试，无需任何外部依赖
make test-e2e       # binary smoke + config parse + matcher fixture 测试
make test-e2e-lab   # 以上 + live sensor 采集验证 + episode 隔离测试
```
