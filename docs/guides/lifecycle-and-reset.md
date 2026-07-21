# Lifecycle And Reset

## Validate

`validate` 解析 HCL、求值非 secret 输入、构建 graph，并检查 schema、引用、provider 配置和 capability requirement。它不应创建外部资源。

## Plan

`plan` 将 desired graph 与 state/observation 比较，输出 create、read、no-op、replace、delete 或 unknown。先理解 action reason，再允许 mutation。

常见 replacement 原因包括 image digest、provider immutable config、network attachment 或资源地址变化。Unknown 表示无法可靠观察，不是资源缺失。

## Apply

`apply` 执行已验证的有序 action。Stored plan 在 provider 调用前校验 HCL、state serial、schema、driver、artifact 和 variable fingerprint。任一输入变化要求重新 plan。

重复 apply 应收敛为 no-op。若重复创建，优先检查 observation 或 persisted external identity，不要在 provisioner 中掩盖问题。

## Reset

Reset 用 immutable baseline 替换 mutable guest，不改变 topology intent：

```bash
sysbox -f range.hcl reset --auto-approve
sysbox -f range.hcl reset --target sysbox_node.web --auto-approve
```

Reset 后逻辑 address、声明 IP/MAC 与 artifact digest 保持稳定；target external ID 变化；非 target 节点不应被替换。应用级数据是否保留取决于其是否位于被替换 guest 之外的声明资源。

## Destroy

Destroy 按逆依赖顺序删除，并在每个 provider 边界重新验证 ownership。`prevent_destroy` 会让 plan 失败。重复 destroy 应安全收敛。

## Interrupted Runs

中断时保留 state 和 checkpoint。先检查 [Troubleshooting](troubleshooting.md)，再使用当前 CLI 提供的 recovery/cleanup 操作。不要通过删除 state 或 checkpoint 强行“重新开始”，这会丢失安全清理所需的 identity。

## Upgrade Boundary

State schema 硬升级时，使用创建旧 state 的 binary destroy，再用新版本 recreate。直接删除旧 state 只会让 Sysbox 忘记资源，不会清理宿主机对象。
