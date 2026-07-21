# Networking And Policy

## Networks And Attachments

`sysbox_network` 声明 CIDR 和 NAT。Node `link` 与 router `interface` 声明 logical attachment、IP prefix、gateway、MAC 和 aliases。

```hcl
resource "sysbox_network" "dmz" {
  cidr = "10.50.0.0/24"
  nat  = true
}

link "dmz" {
  network = sysbox_network.dmz.id
  ip      = "10.50.0.10/24"
  aliases = ["web"]
}
```

Logical attachment name 是 policy 和 state identity。不要依赖 guest `eth0` 或宿主机 veth 名称。

## Routes And Routers

Node `route` 声明 destination CIDR 与 next-hop。`sysbox_router` 用多个 interface 提供 forwarding，并可用 `nat_from`/`nat_to` 声明 NAT 方向。

路由器是普通受管节点，不应通过宿主机脚本临时注入 route 或 iptables。

## Port Exposure

`target` 是 guest 内端口。`direct` 通过节点 IP 访问；Docker `host` 使用 host binding，需要 `published` 和 managed NAT network。Firecracker/libvirt 当前不提供 host port publishing。

## Firewall Policy

Firewall 绑定 node/router，规则使用 logical attachment、CIDR、port range、protocol、connection state、verdict、counter 和 rate-limited log。

推荐顺序：

1. 明确 default input/output/forward；
2. 允许 established/related 返回流量；
3. 按最小范围允许 new flow；
4. 对管理流量使用独立 CIDR/attachment；
5. plan 后运行真实 packet-level acceptance。

Policy 当前只支持 IPv4。不要在 provisioner 中运行 `iptables`、`nft` 或 `nsenter`；这类规则没有 typed identity、readback 和安全 destroy。

## DNS Aliases

Docker aliases 由 attachment 声明并由 provider wiring。Alias 是网络级 endpoint 属性，不是 container hostname 的替代品。Observation 会把缺失 alias 作为 drift 报告。

## Verification

至少验证允许路径、拒绝路径、返回流量、NAT、跨 provider forwarding、重复 apply 和 destroy 后 namespace/bridge/veth/TAP/nftables residue。
