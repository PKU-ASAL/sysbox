# Sysbox 快速开始

Sysbox 用 HCL 声明并管理 Docker、Firecracker、libvirt 和 Linux 网络实验拓扑。
当前 dev 的 Plan/Apply/Reset/Destroy 和 pause/resume CLI 已改为服务客户端，统一经宿主执行。部分维护入口和部署权限仍待收敛。

只维护三份文档：本快速开始、[架构设计与执行路径清单](docs/architecture.md)、[接口文档](docs/interfaces.md)。

## 环境与构建

从本仓库当前检出的版本开始。需要 Linux、Go 1.26、Bash、jq；本例还需要 Docker Engine。
执行宿主需要访问 Docker；运行客户端的用户不需要 Docker socket、数据库或签名私钥。请使用可丢弃的实验宿主。

```sh
# 在仓库根目录执行。先构建 guest helpers，再用 overlay 嵌入 CLI，不改写仓库中的 helper 文件。
bash scripts/release/build-binaries.sh ./bin
./bin/sysbox version
```

构建支持 Linux amd64/arm64。以下示例声明 amd64 镜像；arm64 宿主使用前将示例的 architecture 改为 arm64，
并确认镜像提供对应平台。预编译制品应先校验随附 SHA256SUMS，不混用不同版本的 CLI 和 helper。

## 使用预编译 Release

发布包只包含 binaries、LICENSE、README 和 build-metadata.json，不包含 examples、scripts 或 docs。
先用下载目录中的 SHA256SUMS 校验对应归档，再解压到一个新目录。无需 Go 编译器；仍需 Linux、Docker、Git 和 jq。
在解压目录执行以下命令，取得与 binary 完全匹配的示例和三份文档：

```sh
sysbox_release_dir="$PWD"
sysbox_release_commit="$(jq -r .commit build-metadata.json)"
sysbox_release_source="$(jq -r .source build-metadata.json)"
git clone "$sysbox_release_source" sysbox-source
git -C sysbox-source checkout --detach "$sysbox_release_commit"
cd sysbox-source
mkdir -p bin
cp "$sysbox_release_dir/sysbox" "$sysbox_release_dir/sysbox-init" "$sysbox_release_dir/sysbox-netns" bin/
./bin/sysbox version
```

随后从下方第一个拓扑开始，不执行源码构建。若发布源取不到 metadata 中的 commit，先解决制品来源不一致，
不要改用最新分支的配置。发布包内的相对文档链接需要在这份对应源码 checkout 中打开。

## 第一个 Docker 拓扑

使用 [docker-service 示例](examples/docker-service/field.sysbox.hcl)，它创建 nginx、Docker NAT 网络和本机 18080 端口。
先准备同版本 API、Agent 和 host-supervisor，并按[宿主协议](docs/interfaces.md#host-protocol)配置身份、密钥和共享存储。
本例连接已经启动的服务；CLI 不再隐式启动本地执行器。确认执行宿主的 `172.31.10.0/24` 和 18080 端口未被占用。

```sh
export SYSBOX_API_URL=http://127.0.0.1:9876
# 如服务启用了 token，设置 SYSBOX_API_TOKEN。
./bin/sysbox plan --topology quickstart -f examples/docker-service/field.sysbox.hcl
./bin/sysbox apply --topology quickstart -f examples/docker-service/field.sysbox.hcl --auto-approve
curl --fail http://127.0.0.1:18080/
./bin/sysbox plan --topology quickstart -f examples/docker-service/field.sysbox.hcl
```

curl 地址应指向实际执行宿主。Plan 比较配置和服务中的 state；实际资源核对由宿主执行。
主 HCL 外的脚本/模块通过 `--include 相对路径` 明确上传；输入变量通过 `--inputs values.json` 单独发送。
同一个 topology 始终使用服务指定的存储，不再传 `--state` 或 `--backend`。

## 清理与失败处理

```sh
./bin/sysbox destroy --topology quickstart --auto-approve
./bin/sysbox run show RUN_ID
./bin/sysbox run wait RUN_ID
./bin/sysbox run cancel RUN_ID
```

提交前会打印请求 key，受理后打印 Run ID。回复丢失时用相同 `--key` 和原请求重试；改配置或重新执行用新 key。
默认等待结果，`--detach` 在受理后返回。Ctrl-C 仅停止等待；需要取消时显式调用 run cancel，并查询结果确认。
遇到 unknown/未确认停止，保留服务中的 state 和 journal，按[恢复契约](docs/interfaces.md#recovery-contract)处理，不能删库重来。

## API、Agent 与其他 Provider

Agent 现在必须以普通用户运行，只负责 API 与宿主服务之间的通信。它没有 Docker socket、数据库凭据或签名私钥。
`deploy/docker/compose.agent.yml` 和 `deploy/systemd/sysbox-agent.service` 都按这个边界配置。
宿主服务仍以 root 运行，持有 provider 权限与 host 私钥。

首次部署先完成下面四步；已有环境按“停止旧服务 → 确认并清理旧资源 → 归档旧数据 → 重新部署”迁移，不覆盖旧身份和密钥。

1. 宿主准备 Python 3（安装脚本使用），构建同版本二进制，创建 UID/GID 32000 的 `sysbox-agent` 用户，将二进制装到 `/usr/local/bin/sysbox`。
   用 `sudo sysbox init --authority-dir /etc/sysbox-api` 生成新密钥。
   建立 root 专用的 `/etc/sysbox-host`，把生成的 `host.key` 移到该目录；API 目录只保留 issuer 私钥和公钥材料。
2. 配置并启动 API。Compose 模板使用 `/etc/sysbox-api/issuer.key` 和 `host.pub`，默认 Agent ID 为 `local-docker`。
   设置 `SYSBOX_API_TOKEN`、数据库密码和对应配置；不要把这些环境变量传给 Agent。
3. 用管理员身份注册，再安装 worker 所需文件：

   ```sh
   make .agent-register AGENT_ID=local-docker AGENT_SUBSTRATES=docker
   sudo deploy/prepare-worker.sh .sysbox/enrollment/identity.json /etc/sysbox-api/issuer.pub /etc/sysbox-api/host.pub
   ```

   注册使用 `SYSBOX_API_TOKEN`，生成的 identity 只保存 Agent 自己的通信凭据。
   默认 API 地址为 `http://127.0.0.1:9876`；其他地址通过 `AGENT_API_URL` 指定，须同时能从宿主与 Agent 访问。
4. 将 [宿主配置模板](deploy/systemd/sysbox-host.yaml) 安装为 `/etc/sysbox-host/sysbox.yaml`（root、0600），
   填写与 API 相同的数据库、issuer 公钥及实际 provider 配置。
   安装两个 [systemd 单元](deploy/systemd)，先启动 `sysbox-host`，再启动 `sysbox-agent`。
   使用 Compose worker 时，以 `make api deploy-full` 启动；它要求身份、公钥和宿主 socket 已准备好。

`prepare-worker.sh` 只安装身份与公钥，不启动服务，也不覆盖已有身份。更多字段见[宿主协议](docs/interfaces.md#host-protocol)。

Firecracker 只通过 jailer 启动，需要 root 宿主服务、配套的 Firecracker/jailer 1.15.1、KVM、cgroup v2 委派，以及匹配架构的 kernel/rootfs 和 helper。安装要求见[接口文档](docs/interfaces.md#hcl-firecracker-provider-块)。
Libvirt 需要可用的 libvirt/QEMU、guest 镜像和管理权限。Windows 持久任务需 QGA 和单节点安装的签名任务 helper。
支持范围、文件/任务限制和升级要求见 [接口文档](docs/interfaces.md)。

## 开发验证

```sh
go test ./...
go vet ./pkg/... ./cmd/...
make docs-test
python3 scripts/acceptance/campaign.py check
python3 -m unittest discover -s scripts/acceptance -p 'test_*.py'
```

默认 Go 测试不等于真实 provider/Windows/断电验收；受环境限制而 skip 的测试不能算通过对应能力门槛。
真实宿主测试只在隔离实验环境运行。发布构建与验收入口：
[scripts/release](scripts/release)、[scripts/acceptance](scripts/acceptance)、[Makefile](Makefile)。
Release 镜像应使用已校验归档并核对 CLI/helper 哈希；同源构建不能替代同制品验收。

## 历史文档

原有文档完整保存在 [归档](docs/archive/2026-10-27-documentation.tar.gz)，
原路径和 SHA-256 在 [归档清单](docs/archive/2026-10-27-documentation.json)。
归档中的历史设计、完成声明和命令不覆盖当前三份文档；使用恢复记录前核对来源版本。
