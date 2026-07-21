# Investigation Guide

## Start With Plan

```bash
sysbox -f topology.hcl validate
sysbox -f topology.hcl plan
```

Plan reason 区分配置变化、外部缺失、drift、degraded observation 和 replacement。不要在未理解 replacement 原因时直接 apply。

## Inspect State

```bash
sysbox state list
sysbox state show sysbox_node.web
sysbox state get sysbox_node.web.primary_ip
```

核对 canonical address、external ID、artifact digest、attachment 和 observation status。Provider-private payload 不应作为用户调试接口。

## Runs And Checkpoints

失败 mutation 会留下 run/checkpoint。先保留现场并查看最后完成的 operation step；恢复会重新观察外部对象，然后采用、继续或清理。不要手工删除 checkpoint 后再次 apply。

API 模式下同时检查 run events、Agent heartbeat、command lease 和 inventory。HTTP 422 表示配置 diagnostics；409 通常表示 state serial、plan fingerprint 或 lease 冲突。

## Residue Audit

Destroy 后按 topology ownership label 检查容器、VM、network namespace、bridge、veth、TAP、socket、overlay 和 nftables table。发现未归属对象时不要扩大删除范围，应记录 external ID 与 ownership marker 后修复 provider contract。
