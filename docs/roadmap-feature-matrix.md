# Sysbox Roadmap & Feature Matrix

> Reference for product roadmap and prioritisation.
> Last updated: 2026-05-10

---

## 1. Sysbox vs Terraform — Feature Matrix

### 1.1 Configuration Language

| Feature | Terraform | Sysbox (now) | Gap / Notes |
|---|---|---|---|
| Config language | HCL 2.0 full spec | HCL subset | Missing type system, validation |
| Variable (input) | `var.x` + `.tfvars` files | Partial | No `.tfvars` file support |
| Output block | `output {}` with value | Partial | No post-apply printing |
| Local values | `locals {}` | ❌ | Needed for DRY expressions |
| Data source | `data {}` | ❌ | Read-only external lookups |
| Module system | `module {}` (local + registry) | ❌ | Lab topology reuse |
| Workspace | `terraform workspace` | ❌ | Multi-environment isolation |
| Built-in functions | 70+ (cidrsubnet, file, …) | Partial (cidr) | Most missing |
| Expressions / loops | `for`, `for_each`, `count` | ❌ | Dynamic resource generation |
| Sensitive values | `sensitive = true` in output | ❌ | Keys currently plaintext |

### 1.2 Resource Model

| Feature | Terraform | Sysbox (now) | Gap / Notes |
|---|---|---|---|
| Resource types | Provider-defined, unlimited | node / network / image | Need: agent, subnet |
| Implicit dependency | Reference → graph edge | ✅ | |
| Explicit `depends_on` | ✅ | ❌ | Provisioner ordering |
| `count` / `for_each` | ✅ | ❌ | Batch node creation |
| `lifecycle` block | create_before_destroy, prevent_destroy, ignore_changes | ❌ | Lifecycle control |
| Multi-provider | ✅ (per-resource `provider =`) | ❌ | Single Docker provider |
| `taint` / `-replace` | ✅ Force rebuild | ❌ | Manual workaround: destroy + apply |
| `import` | Bring existing resources under management | ❌ | Cannot adopt running containers |
| `moved` block | Rename without destroy/recreate | ❌ | Rename = delete + recreate |

### 1.3 State Management

| Feature | Terraform | Sysbox (now) | Gap / Notes |
|---|---|---|---|
| State persistence | `terraform.tfstate` JSON | ✅ Local JSON via `state.Manager` | Already implemented |
| Remote backend | S3 / GCS / Consul / TF Cloud | ❌ | Optional long-term |
| State locking | DynamoDB / backend-native | ✅ `flock` per-file | Already implemented |
| Drift detection | `terraform plan` shows delta | Basic `Refresh` | Needs completion |
| State manipulation | `state mv / rm / show` | `sysbox state show` only | mv/rm missing |
| `terraform refresh` | Sync live → state | ❌ explicit | Needs `sysbox refresh` command |
| State encryption | TF Cloud option | ❌ | Optional |

### 1.4 Plan / Apply / Provisioners

| Feature | Terraform | Sysbox (now) | Gap / Notes |
|---|---|---|---|
| `plan` (dry-run) | `terraform plan` | `sysbox plan` ✅ | Output format basic |
| `apply` | `terraform apply` | `sysbox apply` ✅ | |
| `destroy` | `terraform destroy` | `sysbox down` ✅ | |
| Targeted apply | `-target=resource.name` | ❌ | Single resource ops |
| Auto-approve flag | `-auto-approve` | `--yes` | Already exists |
| `provisioner "file"` | Copy files to resource | ❌ → **implementing now** | |
| `provisioner "exec"` | `remote-exec` / `local-exec` | ❌ → **implementing now** | Replaces `docker exec` in lab.sh |
| `connection` block | ssh / winrm | ❌ → **implementing now** | Need: docker / vsock / ssh |
| `null_resource` | Pure provisioner trigger | ❌ | Add later |
| `check` block | Post-apply assertions | ❌ | Useful for lab validation |
| `precondition` | Input validation | ❌ | |

### 1.5 Sysbox-Specific (Beyond Terraform Scope)

| Feature | Terraform | Sysbox (now) | Priority |
|---|---|---|---|
| eBPF sensor lifecycle | ❌ | ✅ | Core |
| `sysbox_agent` (ACP) | ❌ | ❌ → **implementing now** | High |
| `sysbox_network` nat=true | Partial (cloud VPC) | ❌ → **implementing now** | High |
| Substrate abstraction (Docker/microVM/VM) | Provider plugin | Docker only | Medium |
| PID tree attribution | ❌ | matcher | Core |
| IoC / TTP rule engine | ❌ | ✅ | Core |
| RL reward signal | ❌ | ✅ | Core |
| Episode runner | ❌ | `run_sdk.py` | Core |

---

## 2. Open-Source Tool Landscape

### 2.1 Summary Table

| Tool | Declarative | Multi-node network | VM | Docker | Provisioners | State | eBPF | Agent API | Open source |
|---|---|---|---|---|---|---|---|---|---|
| Vagrant | ✅ Ruby DSL | ⚠️ host-only/bridged | ✅ VBox/VMware/libvirt | ⚠️ provider | ✅ Shell/Ansible | ❌ local-only | ❌ | ❌ | ⚠️ BSL 1.1 |
| Terraform + Docker | ✅ HCL | ⚠️ Docker bridge | ✅ cloud providers | ✅ native | ⚠️ null_resource | ✅ remote | ❌ | ❌ | ⚠️ BSL 1.1 (OpenTofu ✅) |
| Pulumi | ✅ code | ⚠️ Docker bridge | ✅ cloud providers | ✅ pulumi-docker | ✅ pulumi-command | ✅ cloud/S3 | ❌ | ✅ Automation API | ✅ |
| Testcontainers | ❌ imperative | ⚠️ Docker bridge | ❌ | ✅ native | ⚠️ execInContainer | ❌ ephemeral | ❌ | ⚠️ library | ✅ |
| Dagger | ❌ SDK | ⚠️ service bindings | ❌ | ✅ native | ✅ withExec | ❌ cache | ❌ | ⚠️ preview | ✅ |
| Ansible | ⚠️ procedural | ⚠️ OS-level | ✅ SSH | ✅ community.docker | ✅ rich modules | ❌ stateless | ⚠️ deploy | ⚠️ subprocess | ✅ |
| GOAD | ✅ YAML+Vagrant | ⚠️ L2 only | ✅ VBox/VMware/Proxmox | ⚠️ LXD | ✅ Ansible | ⚠️ Vagrant | ❌ | ❌ | ✅ |
| Ludus | ✅ YAML range | ✅ VLAN per range | ✅ Proxmox KVM | ❌ | ✅ Ansible | ⚠️ snapshots | ❌ | ⚠️ REST | ✅ |
| DetectionLab | ✅ Vagrant+Packer | ⚠️ host-only | ✅ VBox/VMware | ❌ | ✅ Ansible+PS | ❌ local | ❌ Sysmon/Win | ❌ | ✅ (abandoned) |
| MITRE Caldera | ✅ YAML abilities | N/A (C2, not infra) | N/A | N/A | N/A attack | ⚠️ DB | ❌ | ✅ REST | ✅ |

### 2.2 Key Findings

**No existing tool covers the full sysbox scenario.** The critical gaps across all surveyed tools:

1. **eBPF/kernel observability**: Every tool surveyed has zero native eBPF integration. All are infrastructure lifecycle tools; eBPF must be layered on separately.

2. **Agent-as-resource (ACP)**: No tool models an AI coding agent as a deployable infrastructure resource that speaks a standard protocol (ACP) from within a lab node.

3. **Mixed VM + container topologies**: No single tool handles both VM and container lifecycle with proper networking under one declarative config.

4. **Attribution + reward**: PID-tree attribution, IoC/TTP labelling, and RL reward signal generation are completely absent from all surveyed tools.

**Closest combination** for building something sysbox-like from scratch: Pulumi Automation API (lifecycle + AI programmatic control) + Ansible (provisioning) + custom eBPF layer. Requires significant glue code and lacks the unified HCL topology model.

### 2.3 Complementary Tools (Not Replacements)

| Tool | Role alongside sysbox |
|---|---|
| **Ansible** | Sub-provisioner for complex multi-step config (already used in GOAD/Ludus) |
| **MITRE Caldera** | Attack emulation layer on top of sysbox-managed nodes |
| **Tetragon / Cilium** | eBPF observability alternative to custom sensor |
| **Packer** | Build custom VM/container images referenced in `sysbox_image` |

---

## 3. Implementation Roadmap

### 3.1 Immediate (current sprint)

| Item | Description | Status |
|---|---|---|
| `provisioner "exec"` | Substrate-agnostic command execution (replaces `docker exec` in lab.sh) | **in progress** |
| `provisioner "file"` | File injection into nodes (replaces `docker cp`) | **in progress** |
| `connection` block | Auto-selects docker/ssh/vsock per substrate | **in progress** |
| `sysbox_network nat=true` | NAT uplink for internet access (agent LLM calls) | **in progress** |
| `sysbox_agent` | ACP agent as first-class resource, deployed inside node | **in progress** |
| `locals {}` | DRY local value expressions | **in progress** |
| `output` block | Post-apply value exposure (IPs, ports) | **in progress** |
| `depends_on` | Explicit cross-resource ordering | **in progress** |

### 3.2 Near-term

| Item | Description |
|---|---|
| `for_each` batch resources | Create N nodes from a map/list |
| `data {}` source | Read-only lookups (e.g. existing Docker images) |
| `sysbox refresh` command | Explicit live→state sync |
| `state mv / rm` commands | Manual state surgery |
| `check` block | Post-apply assertions (lab health validation) |
| Targeted apply `-target` | Apply a single resource |

### 3.3 Later

| Item | Description |
|---|---|
| Module system | Reusable lab topology packages |
| Multi-substrate | Firecracker microVM + QEMU VM substrate drivers |
| `vsock` connection | Provisioner transport for microVMs |
| Remote state backend | S3 / object storage for team sharing |
| Sensitive value masking | Encrypt secrets in state file |
| `terraform import` equivalent | Adopt existing running containers/VMs |

---

## 4. Connection Abstraction Design

Provisioners need a substrate-agnostic "how to reach the node" mechanism.

```hcl
resource "sysbox_node" "node_attack" {
  # ...
  connection {
    type = "auto"   # auto = docker when substrate=docker
                    # auto = ssh when substrate=firecracker/vm
  }

  provisioner "file" {
    source      = "configs/opencode.json"
    destination = "/root/.config/opencode/config.json"
  }

  provisioner "exec" {
    inline = [
      "mkdir -p /root/.ssh",
      "chmod 700 /root/.ssh",
    ]
  }

  provisioner "exec" {
    background = true
    inline     = ["while true; do echo mock | nc -l -p 5432; done"]
  }
}
```

Connection type → implementation mapping:

| `type` | Substrate | Implementation |
|---|---|---|
| `"auto"` or `"docker"` | Docker | `docker exec` via Docker API |
| `"ssh"` | Any | Standard SSH (for VMs) |
| `"vsock"` | Firecracker | virtio-vsock (stub, Phase 3) |

---

## 5. sysbox_agent Resource Design

```hcl
resource "sysbox_agent" "red" {
  node    = sysbox_node.node_attack.id
  command = ["opencode", "serve", "--port", "4096"]
  port    = 4096
  env = {
    ANTHROPIC_API_KEY  = var.anthropic_api_key
    ANTHROPIC_BASE_URL = var.anthropic_base_url
  }
}
```

Runtime behaviour:
1. `apply`: verify node is running → `docker exec -d` the agent command → record host PID in state
2. `destroy`: kill the agent process by PID
3. State records: `node`, `pid`, `port`, `container_id`

The agent runs **inside** the node container. The episode runner reaches it via HTTP at `node_ip:port` using the ACP protocol (`initialize` → `session/new` → `session/prompt`).

---

## 6. net_uplink (NAT) Network Design

```hcl
resource "sysbox_network" "net_uplink" {
  cidr = "172.20.0.0/24"
  nat  = true
}

resource "sysbox_node" "node_attack" {
  # ...
  link {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
    gw      = "10.0.1.254"
  }
  link {
    network = sysbox_network.net_uplink.id
    ip      = "172.20.0.10/24"
    gw      = "172.20.0.1"
  }
}
```

Implementation:
- `nat=false` (default): current netns/bridge/veth approach (isolated)
- `nat=true`: Docker-native bridge network (`docker network create --driver bridge --subnet <cidr>`) + `docker network connect` at node creation time. Docker handles iptables MASQUERADE automatically.
