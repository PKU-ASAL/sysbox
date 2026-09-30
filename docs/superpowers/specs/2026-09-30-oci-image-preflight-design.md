# OCI 镜像只读预检修复

## 目标与边界

补齐 `sysbox_image kind = "oci"` 的资源级 preflight。检查目标是 apply 使用的 Docker daemon，而不是浏览器所在机器。检查不 pull、不 load、不访问 registry；本地未缓存不阻塞首次 apply。

独立分支：`fix/oci-image-preflight`，基于 `main` 的 `2e612e3`。不修改 `/tmp/sysbox-refactor`，不合并主线程重构，不发布镜像或修改消费方版本。

## 方案选择

1. **推荐：可选的 artifact 只读检查接口。** Docker provider 实现检查，runtime 解码配置、选择 artifact driver 并汇总结果。保持现有 `driver.Artifact.ResolveImage` 契约不变，其他 provider 不必为此次修复增加空实现。
2. runtime 直接调用 Docker SDK 或命令：改动少，但把 provider 细节引入通用层，不采用。
3. 复用 `ResolveImage`：可能 pull，违反只读约束，不采用。

## 接口与数据流

在 driver 包新增可选 `ArtifactPreflight` 接口：

```go
type ArtifactPreflight interface {
    PreflightImage(context.Context, substrate.ArtifactSource) []substrate.PreflightCheck
}
```

runtime 保留当前 HCL/API 公共结构；OCI 分支通过配置中的 substrate 选择 artifact driver，并探测可选接口。检查项命名为 `image:<resource-name>:oci`。provider 不支持检查时返回明确的 warning，不能悄悄略过。driver 解析失败应报告 error。非 OCI 的本地文件和 URL 预检保持原样。

Docker 检查使用 provider 已配置的 client，复用现有 digest 归一化规则，比较 `img.ID`，不改成 `RepoDigests`。通过有界 context 防止预检挂起；现有 hook 不携带请求 context，此次使用局部 5 秒超时，不为此迁移整个预检接口。

source 若是执行期才能解析的 secret 引用，不把引用字面量当镜像名，也不尝试 pull；返回无法在本阶段检查的 warning，不暴露 secret。

## 结果规则

| 情况 | 结果 |
| --- | --- |
| 已缓存且 SHA 与 image ID 一致 | `ok: true`，info |
| 已缓存但 SHA 不一致 | `ok: false`，error，列出实际和期望摘要 |
| 已缓存且未配置 SHA | `ok: true`，info，明确未钉住摘要 |
| Docker 明确返回镜像 NotFound | `ok: true`，warning |
| 权限、连接、超时或其他 inspect 错误 | `ok: false`，error，不伪装为未缓存 |

未缓存提示必须包含镜像引用，并说明 apply 会尝试按名 pull。hint 给出两条路径：本地构建镜像先构建或用 `docker load` 导入到目标 daemon；或者改用该 daemon 可拉取的镜像引用。不得声称离线检查已经判定 registry 镜像不存在。

warning 不改变 API 整体成功状态；在其余检查通过时，未缓存拓扑整体 `ok: true`。preflight 只是检查时刻的快照，apply 的摘要校验仍然保留。

## 验证计划

先写失败测试，再实现。使用假的 Docker HTTP transport 或测试服务，不依赖真实 daemon 或 registry。覆盖已缓存匹配、不匹配、未钉 SHA、NotFound、权限错误、连接错误、超时、摘要归一化。对所有检查断言没有 pull/load 请求。

runtime 测试覆盖 OCI 调用正确 driver、检查名称、无检查能力的 warning、secret 引用无法检查的 warning，并保持非 OCI 测试通过。API 测试覆盖未缓存 warning 不令整体失败，digest mismatch 令整体失败。

执行相关 provider/runtime/API 测试以及 diff 检查；记录环境导致的阻塞，不将未运行的测试说成通过。

## 不包含

- OCI tar/URL 导入与归档摘要设计。
- apply 的“所有 inspect 错误均触发 pull”相邻问题。
- repository、observation-driven 引擎、Firecracker 重构。
- 版本发布、推送远端或修改 cyberfield 的 SYSBOX_IMAGE。

## 进度

设计已获用户确认，修复和回归测试已在独立分支完成。现有 HCL、HTTP JSON 与 `Artifact.ResolveImage` 接口保持不变。生产改动仅涉及可选 driver 接口、Docker 只读检查和 runtime 分发；没有修改 apply。

验证经过失败测试再实现：原代码的 Docker 测试缺少检查接口，runtime/API 测试没有镜像检查项；实现后均通过。新增 Docker 测试使用内存 HTTP transport，断言唯一请求为目标镜像的 GET inspect，不连接真实 daemon 或 registry。

已通过：

- provider/runtime/API 的所有 `Preflight` 测试，连续运行 5 次。
- provider/runtime/API 的 `Preflight` race 检查。
- driver/runtime 完整测试及完整 race 检查。
- `go test ./... -run '^$'` 全仓库编译检查（不代表全仓库测试执行）。
- `gofmt`、`git diff --check` 及按设计逐项人工复核。依侧会话约束，没有调用审查子代理。

完整 Docker/API 包测试已尝试，但旧测试需要 `httptest.NewServer` 监听本地端口，被沙箱以 `socket: operation not permitted` 拒绝：分别停在 `TestDockerObserveReportsMissingAliasAsDrift` 与 `TestAgentCommandWebSocketReceivesAssignedRunAndReportsAck`。未为此修改旧测试或扩大权限；需在允许本地监听的 CI/开发环境补跑。

全部 Go 命令使用 `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/tmp/sysbox-oci-go-build` 和 `-mod=readonly`，未下载依赖。尚未做真实 Docker 联调、合并、推送或版本发布。
