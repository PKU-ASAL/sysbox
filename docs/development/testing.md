# Testing

## Standard Gates

```bash
make docs-test
go test ./...
go vet ./...
make release-test
make release-workflow-test
```

对并发、state、runtime 和 provider 公共路径运行对应 `go test -race`。

## Example And Contract Tests

构建示例必须通过 validate/plan。Shell 脚本运行 `bash -n`，Go 源码运行 `gofmt`。Release tests 验证归档、checksum、metadata、OCI identity 和 GitHub workflow contract。

`make docs-test` 固定正式文档集合，限制 README 重新膨胀，并检查相对链接和已退役路径。新增正式主题时必须有明确单一事实归属，同时更新该门禁。

## Privileged Tests

```bash
make test-privileged-container
```

该门禁验证真实 netns、bridge、veth、TAP、Docker、nftables policy、readback、checkpoint recovery 和 residue cleanup，需要受控 Linux 宿主机权限。

## Heterogeneous Acceptance

```bash
make test-heterogeneous-matrix
make test-heterogeneous-reset
```

Matrix 验证 Docker、Firecracker 和 libvirt 节点共用 IPv4 网络的六个有向通信路径、重复 plan 与 destroy。Reset 验证三次完整 reset、逐 provider targeted reset、external identity replacement、稳定地址/artifact identity，以及最终零归属残留。

这些测试需要 KVM、Firecracker、libvirt、Docker 和准备好的 artifact，不能在不可信 pull request runner 上执行。

## Evidence Standard

验收报告至少记录 commit、宿主机能力、artifact digest、运行命令、通过断言和 residue audit。结果必须来自同一 release commit，不能以服务端口开放代替完整生命周期验证。
