# sysbox

> AI 红队的 Terraform —— 一键搭起 Linux 攻防战场。

**Status:** Phase 1 MVP — Docker container + linux-bridge topology management.

See [docs/specs/2026-05-07-sysbox-design.md](docs/specs/2026-05-07-sysbox-design.md) for the full design.

## Current capabilities (Phase 1)

- HCL declarative topology with substrate/node/network/image resources
- Docker substrate: create/start/stop/destroy containers, inject networks via veth
- linux-bridge networking: netns + bridge + veth + IP assignment + default gateway
- `sysbox init / plan / apply / destroy / state list / show / output` CLI
- State file with atomic save + file lock

**Not yet (Phase 2+):** sensors, cgroup sessions, prediction matcher, Firecracker, libvirt,
SSH access sugar, firewall rules, replay bundle.

## Requirements

- Linux kernel with netns support (any modern distro)
- Docker daemon running and reachable
- Go 1.22+
- Root/sudo when running `apply`/`destroy` (needed for netlink)

## Build

```bash
make build
# => bin/sysbox
```

## Quickstart (Hello World field)

```bash
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json init
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json apply
docker exec sysbox-node_a ping -c 1 10.0.99.20
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json destroy
```

## Testing

```bash
make test                                                # unit tests (no docker needed)
sudo -E make e2e                                         # e2e test (requires docker + root)
go test -tags=docker ./pkg/provider/docker/...           # docker-specific unit tests
sudo -E go test -tags=netns ./pkg/provider/network/...   # netns-specific
```
