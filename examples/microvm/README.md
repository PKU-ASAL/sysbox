# microvm example

Three Firecracker microVMs on isolated networks, with a Docker router doing
NAT between them. Demonstrates:

- The **firecracker substrate** at parity with docker (provisioner runs,
  config injection, no sshd required in the rootfs)
- The **`sysbox_kernel` resource** for URL-based kernel fetching with
  sha256 verification and content-addressed cache
- **`sysbox_image.rootfs` URL support** (symmetric with `docker_ref`)
- **`chain_init`** to work around incomplete rootfs init systems

## Topology

```
                            +---------------------+
                            | host (linux-bridge) |
                            +----------+----------+
                                       |
                       +---------------+----------------+
                       |               |                |
                  net_dmz         net_internal     net_uplink_fc (NAT)
                  10.0.11/24      10.0.12/24       172.22.0/24
                       |               |                |
                  node_attack     node_web/node_db   (host gateway)
                  (firecracker)   (firecracker)
                       |               |
                       +-----[router.core (docker, NAT)]----+
```

## Prerequisites

| Tool | Reason |
|---|---|
| `firecracker` (in `$PATH`) | microVM runtime |
| `mkfs.ext4`, `losetup`, `mount` | sysbox-init builds an ext4 config drive at apply time |
| root | netlink (tap/veth/netns), loop mounts, KVM |
| `/dev/kvm` accessible | firecracker requires KVM |

No `sshd` or cloud-init is required inside the rootfs. The provisioner
talks to a small agent (sysbox-init's vsock-rpc child) over AF_VSOCK
instead.

## How it works

### Boot path

```
sysbox apply
  ├─ pkg/artifact: fetch kernel/rootfs URLs into ~/.cache/sysbox/artifacts/
  ├─ pkg/provider/firecracker:
  │    ├─ copy per-VM rootfs
  │    ├─ inject embedded /sysbox-init into rootfs (loop mount)
  │    ├─ mkfs.ext4 a 4 MiB config.ext4 with VMConfig JSON
  │    ├─ wire boot_args: init=/sysbox-init ip=<client>::<gw>:<mask>:<host>:<dev>:off
  │    └─ start firecracker with rootfs (vda) + config (vdb) + vsock device

guest (PID 1: /sysbox-init)
  ├─ mount /proc /sys /dev
  ├─ mount /dev/vdb → /sysbox-config (ext4 ro)
  ├─ apply hostname / env from config.json
  ├─ apply `ip=` from /proc/cmdline (independent of CONFIG_IP_PNP)
  ├─ spawn vsock-agent (Setsid child, port 8901)
  └─ syscall.Exec(chain_init)   # default /sbin/init; falls back to /bin/sh

host (provisioner)
  └─ VsockConnection.dial(/tmp/fc-images/sysbox-<node>/firecracker.sock)
        └─ "CONNECT 8901\n" → vsock-rpc: ping / exec / write_file
```

### Why `sysbox_kernel` and not `kernel = "/path"`

| | inline path | sysbox_kernel |
|---|---|---|
| Source | local file only | URL / local / file:// |
| Integrity | none | optional sha256 |
| Caching | none | `~/.cache/sysbox/artifacts/<sha-or-urlhash>/` |
| Sharing | repeat per node | one resource, many nodes |
| State tracking | no | yes — drift detection works |
| Future extensibility | dead end | `cmdline_template`, `modules_url`, `dtb`, … |

Inline paths still work for backwards compatibility (`kernel = "/tmp/vmlinux"`)
but the new resource form is the recommended path.

## HCL fields you'll touch

```hcl
# Fetched once, cached forever. sha256 is optional but recommended for
# reproducibility.
resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.10/x86_64/vmlinux-5.10.225"
  sha256    = ""              # set to pin a build
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc
  rootfs    = "/tmp/fc-rootfs.ext4"  # local path
  # rootfs  = "https://example.com/alpine-fc-rootfs.ext4"  # or URL
  # sha256  = "..."
}

resource "sysbox_node" "node_db" {
  substrate = substrate.firecracker.fc
  kernel    = sysbox_kernel.fc_510.id   # ← reference, not path
  image     = sysbox_image.alpine_vm.id
  vcpus     = 1
  memory    = "256"

  # Optional: rootfs has no working /sbin/init? Skip it and just drop to
  # a shell after sysbox-init finishes setup. sysbox-init's vsock-agent
  # keeps running regardless.
  chain_init = "/bin/sh"

  link "internal" {
    network = sysbox_network.net_internal.id
    ip      = "10.0.12.20/24"
    gw      = "10.0.12.254"
  }

  provisioner "exec" {
    inline = ["uname -a", "ip addr"]
  }
}
```

## Artifact cache

Default location (first that exists / is set):

1. `$SYSBOX_CACHE/artifacts/`
2. `$XDG_CACHE_HOME/sysbox/artifacts/`
3. `~/.cache/sysbox/artifacts/`

Layout:

```
artifacts/
  <sha256-or-urlhash>/
    vmlinux-5.10.225           # actual blob, content-addressed
    alpine-rootfs.ext4
```

- Hits are deterministic by sha256 (when supplied) or by URL hash (when not).
- `sysbox destroy` removes state but **not** cache files — they are shared.
- Corruption is self-healing: sha mismatch on a cached file triggers
  re-download.
- Manual prune: `rm -rf ~/.cache/sysbox/artifacts/<key>` (no `cache prune`
  CLI yet).

## Kernel CONFIG requirements

The firecracker substrate is **kernel-agnostic** for IP configuration
(sysbox-init applies `ip=` itself via `ip(8)`). It does, however, require
the following compiled in (**=y, not =m**, because firecracker cannot load
modules):

| CONFIG | Why |
|---|---|
| `CONFIG_VIRTIO_BLK` | rootfs + config drive |
| `CONFIG_VIRTIO_NET` | network |
| `CONFIG_VSOCKETS`, `CONFIG_VIRTIO_VSOCKETS` | sysbox-init's vsock-agent (provisioner channel) |
| `CONFIG_EXT4_FS` | rootfs + config drive filesystem |

Ubuntu/Debian generic kernels ship most of these as `=m` and so will boot
but **fail the vsock-agent**. Use the firecracker team's reference kernel
(the `sysbox_kernel` source above) or build your own from
[firecracker's microvm-kernel-ci config](https://github.com/firecracker-microvm/firecracker/blob/main/resources/guest_configs/).

## Rootfs requirements

The rootfs needs:

- `/bin/sh` or whatever `chain_init` you specified, executable
- `ip` (busybox/iproute2) — for sysbox-init's network setup
- `mount` — for /proc, /sys, /dev (busybox suffices)

It does **not** need:

- `sshd` (we use vsock-rpc)
- `cloud-init` (sysbox-init handles config)
- A full init system if you set `chain_init = "/bin/sh"`

### Recommended: ubuntu-24.04 via the official squashfs

Firecracker upstream maintains a tested Ubuntu 24.04 squashfs alongside
each `vmlinux` release. We provide a helper that mirrors the procedure
documented in [`docs/firecracker-artifacts.md`](../../docs/firecracker-artifacts.md):

```bash
./scripts/prepare-fc-rootfs.sh
# → ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4 (1 GiB, cached)
```

The script:
- downloads `ubuntu-24.04.squashfs` from `firecracker-ci/v1.14/x86_64/`
- `unsquashfs` → ext4 (`mkfs.ext4` + `mount` + `cp -a`)
- caches both squashfs and ext4 under `~/.cache/sysbox/rootfs/`

Reference it from HCL:

```hcl
resource "sysbox_image" "ubuntu_vm" {
  substrate = substrate.firecracker.fc
  rootfs    = "/root/.cache/sysbox/rootfs/ubuntu-24.04.ext4"  # or your $HOME
}
```

### Minimal alpine alternative

If you want a small rootfs and you're OK with `chain_init = "/bin/sh"`:

```bash
docker run --rm --privileged \
  -v "$PWD":/out alpine:latest sh -c '
    apk add --no-cache util-linux iproute2 e2fsprogs &&
    truncate -s 200M /out/rootfs.ext4 &&
    mkfs.ext4 -F /out/rootfs.ext4 &&
    mkdir /m && mount /out/rootfs.ext4 /m &&
    apk -X http://dl-cdn.alpinelinux.org/alpine/edge/main \
        -U --allow-untrusted --root /m --initdb add alpine-base busybox iproute2 &&
    umount /m'
```

## Quick start

```bash
# 1. Build sysbox (also cross-compiles sysbox-init for linux/amd64)
make build

# 2. Prepare the rootfs (cached; subsequent runs are no-ops)
./scripts/prepare-fc-rootfs.sh

# 3. Point HCL at the cached rootfs
#    (edit examples/microvm/field.sysbox.hcl → rootfs = "$HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4")

# 4. Apply — first run also downloads kernel into ~/.cache/sysbox/artifacts/
sudo -E ./bin/sysbox apply \
  -f examples/microvm/field.sysbox.hcl \
  --state .sysbox/runs/microvm/state.json \
  --auto-approve

# 5. Inspect
sudo ./bin/sysbox state -f .sysbox/runs/microvm/state.json
ls ~/.cache/sysbox/artifacts/ ~/.cache/sysbox/rootfs/

# 6. Destroy (cache files persist for reuse)
sudo ./bin/sysbox destroy \
  -f examples/microvm/field.sysbox.hcl \
  --state .sysbox/runs/microvm/state.json \
  --auto-approve
```

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `Unknown kernel command line parameters "ip=..."` | Kernel without `CONFIG_IP_PNP=y` | Harmless — sysbox-init applies it itself |
| `vsock-agent: vsock socket: address family not supported` | `CONFIG_VSOCKETS=m` or missing entirely | Use firecracker-ci kernel (the URL in this example) |
| `can't run '/sbin/openrc': No such file or directory` (looping) | Incomplete alpine rootfs | Set `chain_init = "/bin/sh"` |
| `ioctl(TUNSETIFF): Device or resource busy` | Stale tap from previous run | Already idempotent in recent builds; if it persists, `sudo ip link del tap-...` |
| Apply hangs on `waiting for vsock-agent` | vsock unavailable in guest kernel | See row 2 above |
| `kernel fc_510: sha256 mismatch` | Cached file corrupted or wrong sha pinned in HCL | Delete `~/.cache/sysbox/artifacts/<sha>/` and retry; or correct the sha |

## See also

- [`examples/three-nodes/`](../three-nodes/) — Docker-only equivalent
  topology
- [`pkg/artifact/`](../../pkg/artifact/) — URL/file resolver implementation
- [`cmd/sysbox-init/`](../../cmd/sysbox-init/) — guest PID-1 wrapper
- [`pkg/provider/firecracker/`](../../pkg/provider/firecracker/) —
  substrate implementation
