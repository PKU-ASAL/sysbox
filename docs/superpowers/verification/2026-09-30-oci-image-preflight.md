# OCI preflight 实机验收记录

日期：2026-09-30。分支：`fix/oci-image-preflight`。被验收的生产修复：`55a6ae5`。此次后续变更仅增加测试和文档，没有修改生产代码。

## 结论

Docker/API 普通完整包测试以及启用 race 的完整包测试均返回成功；其中 7 项原有条件化测试因缺少专用环境而跳过，详见下文，不能称为所有测试零跳过。OCI 修复的真实 Docker 验收已执行并通过，不再受此前的沙箱阻断。

真实 Docker Server 为 `27.5.1`。通过工具的受控执行授权，开放本地测试监听和指定 daemon 的只读访问；未修改 socket 权限或主机配置。

## 测试方式

`pkg/api/preflight_oci_integration_test.go` 中的 `TestPreflightOCIRealDocker` 默认跳过，设置 `SYSBOX_TEST_DOCKER_IMAGE` 后显式启用。启用后 daemon 不可访问或指定镜像未缓存会失败，不会静默跳过。

请求经过真实 HTTP 路由、HCL 解码、runtime 分发和真实 Docker provider。API 使用临时目录及临时 HTTP 监听，不启动后台 supervisor，不连接现有业务 API，不修改已有拓扑。镜像复用主机缓存，不创建夹具镜像。

测试中的 Docker SDK 和 Docker CLI 均连接一个只读网关，网关只向本机 Unix socket 转发 ping、version 和 image-inspect；其他路径或写入方法会被阻止、记录并令测试失败。没有 pull/load/build/tag/run/remove 操作，也不访问 registry。

## 实测结果

| 场景 | 镜像检查 | 整体结果 |
| --- | --- | --- |
| 已缓存，SHA 与 image ID 一致 | info | `ok: true` |
| 已缓存，SHA 与 image ID 不一致 | error | `ok: false` |
| 已缓存，未钉 SHA | info | `ok: true` |
| 未缓存的本地构建风格引用 | warning，含导入/可拉取引用两条提示 | `ok: true` |
| 未缓存的 registry 形式引用 | warning，不探测 registry | `ok: true` |
| `test-L3-tree-alpha` | 6 个目标镜像未缓存，均 warning；路由镜像通过 | `ok: true` |
| `test-L3-five-tier` | 5 个目标镜像未缓存，均 warning；路由镜像通过 | `ok: true` |

两份真实 L3 HCL 从 cyberfield 工作区只读读取并复制到临时工作区。11 个目标镜像的检查项逐个与真实 image inspect 结果对照，并校验名称和处置提示；不是仅静态统计 HCL。这证明缺失镜像会被早期报告，不代表其后续 apply 能成功，也未判断远端可拉取性。

使用缓存镜像完整 ID 跑通一轮；再使用已缓存的 `frrouting/frr:latest` 标签，开启 race 连续运行 3 次，全部通过。带两份 L3 拓扑的每轮记录 30 次 image inspect，禁止的请求为 0。

首轮集成测试曾因网关把 `DOCKER_HOST` 设为 `http://` 而失败：Go SDK 可以使用该地址，Docker CLI 报 `invalid bind address format`。已将测试环境地址修正为 SDK 与 CLI 均支持的 `tcp://` 并重跑通过；这是测试夹具问题，没有修改生产探针。

## 复跑命令

在允许本地监听且可访问 `/var/run/docker.sock` 的开发机或 CI 上，于此修复分支执行。`frrouting/frr:latest` 必须已缓存；也可替换为另一个已有镜像引用或完整 image ID。测试不会自动拉取。当前集成测试只支持本机 Unix-socket daemon。

```bash
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
go test -mod=readonly -race ./pkg/provider/docker ./pkg/api -count=1 -timeout=5m

SYSBOX_TEST_DOCKER_IMAGE=frrouting/frr:latest \
  go test -mod=readonly -race ./pkg/api \
  -run '^TestPreflightOCIRealDocker$' -count=3 -v -timeout=3m
```

可选设置 `SYSBOX_TEST_OCI_TOPOLOGIES` 为两份 `field.sysbox.hcl` 的绝对路径列表，Linux 使用冒号分隔。所提供 HCL 应自包含：测试仅复制 HCL，不复制相对路径的外部文件/模块；它们应在当前主机上最多产生 warning，缓存但摘要不符会令验收失败。

实测使用 `GOCACHE=/tmp/sysbox-oci-go-build`，没有下载 Go 依赖；未推送或发布版本。

## 原有条件化测试：7 项尚未执行

以下 6 项要求 `SYSBOX_TEST_DATABASE_URL` 指向专用 PostgreSQL 测试数据库。测试会创建并清理独立 schema，不应提供生产数据库：

- `TestDestroyHTTPIdempotencyIsAtomicAcrossPostgresServers`
- `TestPostgresAgentCommandRejectsStaleStatusRegression`
- `TestPostgresGlobalRevisionRoundTrip`
- `TestDestroyHTTPIdempotencyRejectsFingerprintConflictAcrossPostgresServers`
- `TestPostgresRunDispatchSurvivesRestartAndIsDeliveredByReconciler`
- `TestPostgresDestroyHTTPIdempotencyAdoptsLegacyRunWithoutRedispatch`

另有 `TestGuestExecutionDockerE2E` 要求专用 API、token、运行中的 owning Agent 和测试节点，配置为 `SYSBOX_GUEST_E2E_API`、`SYSBOX_GUEST_E2E_TOKEN`、`SYSBOX_GUEST_E2E_TOPOLOGY`、`SYSBOX_GUEST_E2E_NODE`。该测试会提交节点执行请求，不能随意选择现有业务拓扑。

这 7 项是数据库/Agent 执行集成测试，不是本次 OCI 只读预检验收的替代项；它们仍需专用环境才能达到整个 API 套件零跳过。
