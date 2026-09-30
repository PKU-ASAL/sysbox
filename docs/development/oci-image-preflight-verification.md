# OCI preflight 实机验收记录

日期：2026-09-30。分支：`fix/oci-image-preflight`。被验收的生产修复：`4d52bad`（移植到 GitHub main 后的提交）。后续通用化和验收变更只涉及测试、文档，没有修改生产代码。

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
| 多镜像拓扑：1 个已缓存、2 个未缓存 | 已缓存镜像 info；两个未缓存镜像分别 warning，保留各自资源名与处置提示 | `ok: true` |

所有 HCL 均由测试自身生成，使用通用镜像引用和随机缺失引用，不读取其他项目的文件，也不按镜像业务前缀筛选资源。这验证缺失镜像会被早期报告，不代表其后续 apply 能成功，也未判断远端可拉取性。

实测覆盖通过完整 image ID 或缓存标签选择夹具镜像。测试要求所有 Docker 请求都属于只读允许列表；镜像选择只由 `SYSBOX_TEST_DOCKER_IMAGE` 提供，不依赖任何特定镜像内容或业务用途。通用夹具已随 driver/runtime/Docker/API 完整包 race 回归通过，并独立开启 race 连续运行 3 次通过；每轮记录 9 次 image inspect，非允许请求为 0。

首轮集成测试曾因网关把 `DOCKER_HOST` 设为 `http://` 而失败：Go SDK 可以使用该地址，Docker CLI 报 `invalid bind address format`。已将测试环境地址修正为 SDK 与 CLI 均支持的 `tcp://` 并重跑通过；这是测试夹具问题，没有修改生产探针。

## 复跑命令

在允许本地监听且可访问 `/var/run/docker.sock` 的开发机或 CI 上，于此修复分支执行。先把 `SYSBOX_TEST_DOCKER_IMAGE` 设置为任意已有镜像引用或完整 image ID。测试不读取镜像文件内容、不运行镜像，也不会自动拉取。当前集成测试只支持本机 Unix-socket daemon。

```bash
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
go test -mod=readonly -race ./pkg/provider/docker ./pkg/api -count=1 -timeout=5m

test -n "${SYSBOX_TEST_DOCKER_IMAGE:-}" || exit 1
go test -mod=readonly -race ./pkg/api \
  -run '^TestPreflightOCIRealDocker$' -count=3 -v -timeout=3m
```

多镜像检查由测试内部构造，不需要外部拓扑目录、业务环境或消费者专用配置。

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
