# Sysbox 快速开始

Sysbox 用 HCL 声明并管理 Docker、Firecracker、libvirt 和 Linux 网络实验拓扑。
Plan/Apply/Reset/Destroy 和 pause/resume 通过 API 与宿主服务执行，CLI 不直接操作 provider。

只维护三份文档：本快速开始、[架构设计与执行路径清单](docs/architecture.md)、[接口文档](docs/interfaces.md)。

## 环境与构建

从本仓库当前检出的版本开始。需要 Linux、Go 1.26、Bash、jq；本例还需要 Docker Engine。
执行宿主需要访问 Docker；运行客户端的用户不需要 Docker socket、数据库或签名私钥。请使用可丢弃的实验宿主。

```sh
# 在仓库根目录执行。先构建 guest helpers，再用 overlay 嵌入 CLI，不改写仓库中的 helper 文件。
bash scripts/release/build-binaries.sh ./bin
./bin/sysbox version
```

`make build` 复用上述构建入口，输出配套的 `bin/sysbox`、`sysbox-init`、`sysbox-netns`；
`make build-all` 输出 `bin/linux-amd64/` 和 `bin/linux-arm64/` 两套制品。两者都不改写受跟踪的 helper 文件。
需要嵌入新 helper 的 Go 测试使用 `bash scripts/release/with-helper-overlay.sh go test ...`；
Firecracker 和 P9 验收目标已自动使用该入口。直接 `go build` 仍只嵌入仓库占位文件，不能用于 VM 运行验收。

构建支持 Linux amd64/arm64。以下示例声明 amd64 镜像；arm64 宿主使用前将示例的 architecture 改为 arm64，
并确认镜像提供对应平台。预编译制品应先校验随附 SHA256SUMS，不混用不同版本的 CLI 和 helper。

## Release 安装与首次部署

Linux 发布包包含二进制、三份文档、Docker 示例、部署模板和 `install.py`，无需拉源码。
先用可信发布渠道取得的 `SHA256SUMS` 校验归档，再解压；包内 `INSTALL-SHA256SUMS` 检查完整性，不替代来源验证。
以下命令在解压目录执行，需要 Python 3、systemd、Docker Compose、`ip`、`nsenter`、`nft` 和 `iptables`。
API、Agent 和宿主必须使用同一 commit 的制品，镜像由 `SYSBOX_IMAGE` 显式指定。

源码开发者若需要完整安装包，在干净 checkout 执行
`bash scripts/release/build.sh --tag v0.0.0 --allow-untagged --output /tmp/sysbox-release`，
校验并解压本机架构归档后，也走下面的流程；这不创建正式发布。

1. 初始化新的 API 密钥，把宿主私钥移到 root 专用的安装输入目录。不会覆盖已有密钥。

   ```sh
   sudo ./sysbox init --authority-dir /etc/sysbox-api
   sudo install -d -m 0700 /root/sysbox-install-inputs
   sudo mv /etc/sysbox-api/host.key /root/sysbox-install-inputs/host.key
   sudo install -m 0600 /etc/sysbox-api/host.pub /etc/sysbox-api/issuer.pub /root/sysbox-install-inputs/
   sudo install -m 0600 deploy/systemd/sysbox-host.yaml /root/sysbox-install-inputs/sysbox.yaml
   install -m 0600 .env.example .env
   ```

   编辑 `.env`，设置匹配的 `SYSBOX_IMAGE`、API token、数据库密码；编辑输入目录的 `sysbox.yaml`，
   填写同一数据库的宿主可达地址及 provider 配置，替换 `REPLACE_PASSWORD`。
   保持默认 Agent ID `local-docker`；改 ID 时须同时更新 API 的 host 公钥绑定。

2. 拉取选定镜像并启动 API。宿主服务尚未安装，不要提前运行 `prepare-worker.sh`。

   ```sh
   docker compose --project-directory "$PWD" --env-file .env -f deploy/docker/compose.yml pull
   docker compose --project-directory "$PWD" --env-file .env -f deploy/docker/compose.yml up -d --no-build
   ```

3. 设置 `SYSBOX_API_TOKEN` 为与 `.env` 相同的 token，注册普通用户 Agent 的通信身份。
   API URL 必须同时能从宿主和 Agent 访问；下例为同一台机器。

   ```sh
   export SYSBOX_API_URL=http://127.0.0.1:9876
   sudo --preserve-env=SYSBOX_API_TOKEN ./sysbox agent register --api "$SYSBOX_API_URL" \
     --token "$SYSBOX_API_TOKEN" --id local-docker --name local-docker --substrates docker \
     --identity /root/sysbox-install-inputs/identity.json
   sudo python3 install.py --inputs /root/sysbox-install-inputs --check
   sudo python3 install.py --inputs /root/sysbox-install-inputs
   sudo systemctl daemon-reload
   sudo systemctl enable --now sysbox-host
   sudo systemctl enable --now sysbox-agent
   ```

安装器先检查包内文件、架构、输入、目标冲突和 UID/GID，再安装二进制、配置、身份及 systemd 单元。
它创建 UID/GID 32000 的 `sysbox-agent`，不覆盖已有文件、不自动启动服务；已有安装按
“停旧服务 → 确认并清理旧资源 → 归档旧数据和安装文件 → 重新部署”处理。
I/O 中断会保留部分安装供检查，重试不会静默覆盖。安装输入目录含宿主私钥，始终保持 root 专用。
`--root /tmp/sysbox-stage` 只用于暂存布局，不创建系统用户，也不能代替实机验收。

安装预检使用包内 `sysbox host-key-check` 复用宿主的严格密钥校验；不会启动 provider。
systemd 在依赖暂未就绪时每 5 秒重试，不耗尽启动次数；配置或版本错误仍在日志中明确报告，须修正后启动。

Agent 只持通信身份和公钥，不持 Docker socket、数据库凭据或签名私钥；特权操作由宿主服务执行。
如选择 Compose worker，使用包内 `deploy/docker/compose.agent.yml`，不要同时启动 systemd Agent。

Firecracker 只通过 jailer 启动，需要 root 宿主服务、配套的 Firecracker/jailer 1.15.1、KVM、cgroup v2 委派，以及匹配架构的 kernel/rootfs 和 helper。安装要求见[接口文档](docs/interfaces.md#hcl-firecracker-provider-块)。
Libvirt 需要可用的 libvirt/QEMU、guest 镜像和管理权限。Windows 持久任务需 QGA 和单节点安装的签名任务 helper。
支持范围、文件/任务限制和升级要求见 [接口文档](docs/interfaces.md)。


## 第一个 Docker 拓扑

使用 [docker-service 示例](examples/docker-service/field.sysbox.hcl)，它创建 nginx、Docker NAT 网络和本机 18080 端口。
先准备同版本 API、Agent 和 host-supervisor，并按[宿主协议](docs/interfaces.md#host-protocol)配置身份、密钥和共享存储。
本例连接已经启动的服务；CLI 不再隐式启动本地执行器。确认执行宿主的 `172.31.10.0/24` 和 18080 端口未被占用。

```sh
export SYSBOX_API_URL=http://127.0.0.1:9876
# 如服务启用了 token，设置 SYSBOX_API_TOKEN。
sysbox plan --topology quickstart -f examples/docker-service/field.sysbox.hcl
sysbox apply --topology quickstart -f examples/docker-service/field.sysbox.hcl --auto-approve
curl --fail http://127.0.0.1:18080/
sysbox plan --topology quickstart -f examples/docker-service/field.sysbox.hcl
```

curl 地址应指向实际执行宿主。Plan 比较配置和服务中的 state；宿主在 Apply 规划时观察选定的受管资源，执行前再次核对。
观察未知时停止执行；依赖替换范围不完整时明确报错，不自动增加删除对象。
主 HCL 外的脚本/模块通过 `--include 相对路径` 明确上传；输入变量通过 `--inputs values.json` 单独发送。
同一个 topology 始终使用服务指定的存储，不再传 `--state` 或 `--backend`。

## 清理与失败处理

```sh
sysbox destroy --topology quickstart --auto-approve
sysbox run show RUN_ID
sysbox run wait RUN_ID
sysbox run cancel RUN_ID
```

提交前会打印请求 key，受理后打印 Run ID。回复丢失时用相同 `--key` 和原请求重试；改配置或重新执行用新 key。
默认等待结果，`--detach` 在受理后返回。Ctrl-C 仅停止等待；需要取消时显式调用 run cancel，并查询结果确认。
遇到 unknown/未确认停止，保留服务中的 state 和 journal，按[恢复契约](docs/interfaces.md#recovery-contract)处理，不能删库重来。

## 开发验证

```sh
go test ./...
go vet ./pkg/... ./cmd/...
make docs-test
python3 scripts/acceptance/campaign.py check
python3 -m unittest discover -s scripts/acceptance -p 'test_*.py'
```

默认 Go 测试不等于真实 provider/Windows/断电验收；受环境限制而 skip 的测试不能算通过对应能力门槛。
真实宿主测试只在隔离实验环境运行，使用固定制品并独立验证回收结果。入口见
[发布脚本](scripts/release)、[验收脚本](scripts/acceptance) 和 [Makefile](Makefile)。
当前验证范围与剩余缺口见[架构文档](docs/architecture.md#发布与验收)。

## 历史文档

历史设计和阶段记录通过 Git 历史查阅，不再随当前文档保存归档副本。
