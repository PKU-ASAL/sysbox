# Troubleshooting

按“输入、计划、状态、外部对象、恢复”的顺序调查，不先修改现场。

## 1. Validate Input

```bash
sysbox -f topology.hcl validate
```

配置错误应包含 source range 和 structured diagnostic。确认当前目录，因为 Docker 相对 bind 和本地 artifact path 会从执行目录解析。

## 2. Read The Plan

```bash
sysbox -f topology.hcl plan
```

记录 action、address 和 reason。意外 replace 常来自 artifact digest、provider config、attachment 或 address 变化。Unknown 通常表示 provider、权限或外部 API 不可观察。

## 3. Inspect State

```bash
sysbox state list
sysbox state show 'sysbox_node.web'
sysbox state get 'sysbox_node.web.primary_ip'
```

核对 canonical address、external ID、artifact digest、dependency、attachment 与 observation status。不要手工修改 private envelope。

## 4. Inspect External Ownership

通过 provider 原生只读命令检查对象是否存在、实际 ID 和 Sysbox ownership marker。名称相同不代表是当前资源。对 VM/process 同时核对 UUID、generation、start time、socket/overlay ownership。

## 5. Inspect Run And Checkpoint

失败 mutation 的 checkpoint 记录最后完成的 substep。API 模式同时检查 run events、Agent heartbeat、command lease 和 inventory。

- HTTP 422：配置 diagnostics；
- HTTP 409：state serial、plan fingerprint、claim/lease 或并发冲突；
- degraded：对象存在但健康/attachment 不完整；
- unknown：无法可靠观察，先恢复权限或 provider connectivity。

## 6. Recover Or Clean Up

只使用当前 CLI/API 暴露的 recovery/cleanup 操作。重复 recovery 应幂等。若 recovery 停在 unknown，保存 checkpoint 和外部 evidence，不要删除 state。

## Residue Audit

Destroy 后按 topology ownership 检查 container、domain、overlay、rootfs copy、process、socket、bridge、veth、TAP、namespace 和 nftables table。发现 residue 时记录 address、external ID 和 owner marker，再修复 provider cleanup contract。
