# CLI Reference

## Global Form

```text
sysbox [global options] <command> [command options]
```

常用全局参数：

- `-f <path>`：HCL 配置文件。
- `--state <path-or-backend>`：state 位置。
- `-var name=value`：输入变量。
- `--allow-unsafe-state`：显式允许不具备 locking/CAS 的 backend；仅限受控单写者环境。

以当前 binary 的 `sysbox help` 和 `sysbox <command> --help` 为参数权威来源。

## Lifecycle Commands

```bash
sysbox validate
sysbox plan
sysbox apply [--auto-approve]
sysbox reset [--target ADDRESS] [--auto-approve]
sysbox destroy [--auto-approve]
```

`validate` 不访问外部资源；`plan` 会结合 state 和 observation；mutation 在执行前重新校验 plan fingerprint 与 backend safety。

## State Commands

```bash
sysbox state list
sysbox state show ADDRESS
sysbox state get ADDRESS.ATTRIBUTE
sysbox state mv SOURCE DESTINATION
```

地址必须使用完整 canonical syntax，包含 module、count 或 string key。

## Other Commands

- `version --json`：输出 release version、commit 和构建信息。
- `import`：通过 resource handler 读取并规范化外部对象。
- recovery 相关命令：恢复或清理中断 checkpoint；执行前应先阅读 [Investigation](../guides/investigation.md)。

命令失败返回非零状态。配置 diagnostics、state conflict、unsupported capability 和 provider failure 是不同错误类别，自动化脚本不应只匹配错误文本。
