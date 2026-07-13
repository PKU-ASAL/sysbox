# Controlled Egress

This topology demonstrates the IPv4-only atomic policy contract. The edge
router masquerades traffic from the `internal` logical attachment to `uplink`.
Its firewall defaults forwarding to drop and permits only new TCP/443 flows
from `10.42.0.0/24`, plus established or related return traffic.

Run:

```bash
make cli plan TOPO=controlled-egress
sudo -E make cli apply TOPO=controlled-egress
sudo -E make cli destroy TOPO=controlled-egress
```

IPv6 CIDRs are rejected explicitly. Domain allowlisting requires a managed L7
proxy and is not part of the firewall policy.
