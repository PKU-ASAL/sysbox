# HCL Reference

本页列出 Sysbox 当前配置结构。示例和工作流见 [Authoring Topologies](../guides/authoring-topologies.md)。

## Top-Level Blocks

| Block | Purpose |
|---|---|
| `substrate TYPE {}` | Register a provider implementation under an alias |
| `variable NAME {}` | Declare input and optional default |
| `locals {}` | Define local expressions |
| `module NAME {}` | Load a child configuration from `source` |
| `data TYPE NAME {}` | Read external data through a data source |
| `resource TYPE NAME {}` | Declare managed topology intent |
| `output NAME {}` | Export an evaluated value |

## Expressions And References

Resource reference uses typed canonical identity:

```hcl
image   = sysbox_image.web.id
network = sysbox_network.dmz.id
```

Modules, `count` and `for_each` produce structural addresses. String instance keys remain JSON quoted. `env("NAME")` creates a secret reference; `env_optional("NAME")` is for non-sensitive optional lookup.

## Substrate

```hcl
substrate "docker" {
  alias = "local"
}
```

Required: type label and `alias`. Provider-specific substrate configuration remains in the block body.

## Common Lifecycle

Resources that support lifecycle accept:

| Field | Type | Meaning |
|---|---|---|
| `prevent_destroy` | bool | Reject delete or replacement planning |
| `ignore_changes` | list(string) | Ignore schema-supported desired paths |

Use ignore sparingly; it does not transfer ownership to another system.

## `sysbox_image`

| Field | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Substrate reference |
| `kind` | yes | `oci`, `rootfs`, `qcow2`, or provider-supported kind |
| `source` | yes | Registry reference, URL or local path |
| `architecture` | yes | Guest architecture such as `amd64` |
| `guest_family` | yes | Guest OS family, currently Linux contracts |
| `sha256` | no | Immutable content digest |
| `size` | no | Expected/generated artifact size |

## `sysbox_kernel`

| Field | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Firecracker-capable substrate |
| `architecture` | yes | Kernel architecture |
| `source` | yes | Kernel source/path |
| `sha256` | no | Immutable digest |
| `cmdline_template` | no | Provider kernel command-line template |
| `depends_on` | no | Additional resource addresses |

## `sysbox_network`

| Field | Required | Default | Meaning |
|---|---:|---|---|
| `cidr` | yes | | IPv4 network prefix |
| `type` | no | provider default | Network implementation type |
| `nat` | no | `false` | Managed outbound NAT intent |
| `lifecycle` | no | | Lifecycle block |

## `sysbox_node`

### Common Fields

| Field/block | Required | Meaning |
|---|---:|---|
| `substrate` | yes | Node provider reference |
| `image` | yes | `sysbox_image` reference |
| `guest_family` | no | Override/validate guest family |
| `vcpus` | no | Common requested CPU count where supported |
| `memory` | no | Common requested memory where supported |
| `env` | no | Guest environment map; secret references allowed |
| `depends_on` | no | Explicit dependency addresses |
| `link NAME` | no | Logical network attachment |
| `port` | no | Service exposure intent |
| `route` | no | Static guest route |
| `connection TYPE` | no | Guest connection parameters |
| `provisioner TYPE` | no | Guest execution or file copy |
| `provider TYPE` | no | Provider-specific configuration |
| `lifecycle` | no | Lifecycle policy |

### `link NAME`

| Field | Required | Meaning |
|---|---:|---|
| `network` | yes | `sysbox_network` reference |
| `ip` | yes | Address with prefix |
| `gw` | no | Default/attachment gateway |
| `mac` | no | Stable declared MAC |
| `aliases` | no | Provider network aliases |

### `port`

| Field | Required | Default | Meaning |
|---|---:|---|---|
| `target` | yes | | Guest port |
| `name` | no | | Logical service name |
| `published` | host exposure | | Host port |
| `protocol` | no | `tcp` | `tcp`, `udp`, `http`, `https` |
| `exposure` | no | `direct` | `none`, `direct`, or supported `host` |
| `host_ip` | no | provider default | Host bind address |

### `route`

`dst` is a destination CIDR; `via` is the next-hop address.

### `connection TYPE`

Supports connection-specific `host`, `user`, `password` and `private_key`. Credentials should use secret references.

### `provisioner TYPE`

Fields include `program`, `args`, `environment`, `working_dir`, `shell`, `source`, `destination` and `background`. Valid combinations depend on provisioner type. Provisioner operates inside the guest and does not own host resources.

## Docker Provider Block

```hcl
provider "docker" {
  privileged   = false
  pid_mode     = "host"
  cgroupns_mode = "host"
  binds        = ["./fixtures:/srv/fixtures:ro"]
  entrypoint   = ["/usr/local/bin/server"]
  command      = ["--listen", ":8080"]
}
```

Relative bind source resolves against the Sysbox process working directory. Omitted ENTRYPOINT/CMD inherits image config; non-empty array replaces it; explicit `[]` clears it. Values are direct argv, not shell strings.

## Firecracker Provider Block

| Field | Required | Meaning |
|---|---:|---|
| `kernel` | topology-dependent | Kernel resource/path |
| `rootfs` | topology-dependent | Rootfs resource/path |
| `chain_init` | no | Guest init chained by `sysbox-init` |
| `ssh_user` | no | Guest SSH user |
| `ssh_pass` | no | Secret reference recommended |
| `ssh_port` | no | Guest SSH port |

Machine sizing and network initialization must match the selected provider capability and guest image.

## Libvirt Provider Block

| Field | Required | Meaning |
|---|---:|---|
| `network_init` | yes | Guest network initialization mode |
| `vcpus` | no | Domain vCPU count |
| `memory` | no | Domain memory |
| `machine_type` | no | Libvirt machine type |
| `disk_size` | no | Overlay/guest disk size |
| `ssh_user`, `ssh_pass`, `ssh_key` | no | Guest access |
| `ssh_authorized_key` | no | Key injected during guest init |

## `sysbox_router`

Required `substrate` and `image`; repeated `interface NAME` blocks contain `network` and `ip`. Optional `nat_from` and `nat_to` refer to interface labels. Router supports lifecycle policy.

## `sysbox_firewall`

Top-level fields: `attach_to`, optional `family`, `default_input`, `default_output`, `default_forward`, and repeated `rule NAME`.

Rule fields:

- required `direction` and `verdict`;
- optional source/destination CIDRs and ports;
- optional protocol, input/output logical attachment and connection states;
- optional counter and log.

Current policy family is IPv4. Unsupported IPv6 input fails validation.

## `sysbox_ssh_access`

Required `node` and `authorized_keys`; optional `bind_ip` and `port`. Authorized keys are sensitive execution input and must not leak into logs/state plaintext.

## Import Data Shapes

Import normalization supports provider external IDs such as Docker container name/ID or libvirt domain identity. Imported resources are handler-owned and remain no-op until configuration explicitly changes desired state.
