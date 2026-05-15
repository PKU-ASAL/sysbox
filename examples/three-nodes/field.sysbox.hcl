# Three-node attack lab — sysbox reference topology.
#
# Attack scenario: AI agent (opencode) inside node_attack → DMZ router →
#                  node_web (nginx) → node_db (postgres:16-alpine)
#
#   [host / episode runner]
#        │  ACP HTTP  172.20.0.10:4096
#        ▼
#   node_attack  (10.0.1.10 / 172.20.0.10)
#        │  10.0.1.254 router  │  internet (LLM API)
#   net_dmz                net_uplink (NAT → host → internet)
#        │
#   router.core (10.0.1.254 / 10.0.2.254)
#        │
#   net_internal (10.0.2.0/24)
#     ├── node_web  10.0.2.10   nginx
#     └── node_db   10.0.2.20   postgres:16-alpine

substrate "docker" {
  alias = "light"
}

locals {
  dmz_cidr      = "10.0.1.0/24"
  internal_cidr = "10.0.2.0/24"
  uplink_cidr   = "172.20.0.0/24"
  uplink_gw     = "172.20.0.1"
  router_dmz_ip = "10.0.1.254"
  opencode_port = "4096"
}

# ── Networks ──────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = local.dmz_cidr
}

resource "sysbox_network" "net_internal" {
  cidr = local.internal_cidr
}

# nat=true: Docker bridge with iptables MASQUERADE.
# Gives node_attack internet access for LLM API calls.
# Also accessible from the host at 172.20.0.10 (episode runner connects here).
resource "sysbox_network" "net_uplink" {
  cidr = local.uplink_cidr
  nat  = true
}

# ── Images ────────────────────────────────────────────────────────────────────

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  docker_ref = "alpine:latest"
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.light
  docker_ref = "nginx:alpine"
}

# Pre-built attacker image with opencode + attack tools.
# Build with: docker build -t sysbox-attacker:latest -f Dockerfile.attacker-opencode .
resource "sysbox_image" "attacker" {
  substrate  = substrate.docker.light
  docker_ref = "sysbox-attacker:latest"
}

resource "sysbox_image" "postgres" {
  substrate  = substrate.docker.light
  docker_ref = "postgres:16-alpine"
}

# ── Router (DMZ ↔ internal) ───────────────────────────────────────────────────

resource "sysbox_router" "core" {
  substrate = substrate.docker.light
  image     = sysbox_image.alpine.id

  interface "dmz" {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.254/24"
  }

  interface "internal" {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.254/24"
  }

  interface "uplink" {
    network = sysbox_network.net_uplink.id
    ip      = "172.20.0.254/24"
  }

  nat_from = "internal"
  nat_to   = "uplink"
}

# ── Nodes ─────────────────────────────────────────────────────────────────────

resource "sysbox_node" "node_attack" {
  image     = sysbox_image.attacker.id
  substrate = substrate.docker.light

  # DMZ: lab-internal communication (to router and victim nodes).
  # No gw here — routing is handled by provisioner below.
  link {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
  }

  # Uplink: internet access (LLM API) + reachable from host (episode runner).
  link {
    network = sysbox_network.net_uplink.id
    ip      = "172.20.0.10/24"
  }

  # Copy host SSH pubkey into node so the agent can pivot to victim nodes.
  provisioner "file" {
    source      = env("LAB_SSH_PUBKEY")
    destination = "/tmp/host_pubkey"
  }

  provisioner "exec" {
    inline = [
      # SSH key for agent pivot.
      "mkdir -p /root/.ssh",
      "cat /tmp/host_pubkey >> /root/.ssh/authorized_keys",
      "chmod 600 /root/.ssh/authorized_keys",
      "rm /tmp/host_pubkey",
      # Routing: lab traffic via router, internet via uplink.
      "ip route add 10.0.2.0/24 via 10.0.1.254 2>/dev/null || true",
      "ip route add default via 172.20.0.1 2>/dev/null || true",
    ]
  }
}

resource "sysbox_node" "node_web" {
  image     = sysbox_image.nginx.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.10/24"
    gw      = "10.0.2.254"
  }
}

resource "sysbox_node" "node_db" {
  image     = sysbox_image.postgres.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.20/24"
    gw      = "10.0.2.254"
  }

  env = {
    POSTGRES_DB       = "labdb"
    POSTGRES_USER     = "labuser"
    POSTGRES_PASSWORD = "labpass"
  }
}

# ── eBPF Sensor (tracee sidecar) ─────────────────────────────────────────────
#
# Runs aquasec/tracee as a privileged container sharing the host PID namespace.
# Events are written to /tmp/sysbox-events/ on the host via bind mount.
# lab.sh sensor → sysbox sensor start --sidecar reads from that path.

resource "sysbox_image" "tracee" {
  substrate  = substrate.docker.light
  docker_ref = "aquasec/tracee:latest"
}

resource "sysbox_node" "sensor" {
  image          = sysbox_image.tracee.id
  substrate      = substrate.docker.light
  privileged     = true
  pid_mode       = "host"
  cgroupns_mode  = "host"
  binds = [
    "/tmp/sysbox-events:/tmp/events:rw",
    "/etc/os-release:/etc/os-release-host:ro",
    "/sys/kernel/btf/vmlinux:/sys/kernel/btf/vmlinux:ro",
    "/sys/fs/bpf:/sys/fs/bpf",
    "/sys/fs/cgroup:/sys/fs/cgroup",
    "/var/run/docker.sock:/var/run/docker.sock",
  ]

  provisioner "exec" {
    inline = ["mkdir -p /tmp/events"]
  }
}

# ── Monitor ───────────────────────────────────────────────────────────────────
#
# Declares monitoring intent for all lab nodes. The tracee backend (default)
# scopes to each node's mount namespace, so events are automatically attributed
# to the correct node via tracee's container.name enrichment.
#
# Activated by: sysbox sensor start  (reads this resource from state)
# Swap backend: change backend = "tracee" to "sysdig" / your EDR name.

resource "sysbox_monitor" "lab" {
  backend = "tracee"
  nodes = [
    sysbox_node.node_attack.id,
    sysbox_node.node_web.id,
    sysbox_node.node_db.id,
  ]
  events = [
    "execve", "execveat",
    "openat",
    "connect",
    "accept4",              # sshd accepts inbound connections → background noise
    "clone", "fork", "vfork",
    "sched_process_exit",
  ]
  depends_on = [
    "sysbox_node.node_attack",
    "sysbox_node.node_web",
    "sysbox_node.node_db",
    "sysbox_node.sensor",
  ]
}

# ── Agent ─────────────────────────────────────────────────────────────────────

# Red-team actor: opencode inside node_attack (position=internal).
# The ACP URL is resolved at apply time from the node's Docker network IP.
resource "sysbox_actor" "red" {
  position = "internal"
  node     = sysbox_node.node_attack.id
  command  = ["opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"]
  port     = 4096

  env = {
    DEEPSEEK_API_KEY = env("DEEPSEEK_API_KEY")
  }

  depends_on = ["sysbox_node.node_attack"]
}

# ── Outputs ───────────────────────────────────────────────────────────────────

output "attacker_lab_ip" {
  value       = "10.0.1.10"
  description = "node_attack DMZ IP (lab-internal)"
}

output "attacker_uplink_ip" {
  value       = "172.20.0.10"
  description = "node_attack uplink IP (reachable from host)"
}

output "agent_acp_url" {
  value       = "http://172.20.0.10:4096"
  description = "opencode ACP endpoint for the episode runner"
}

output "web_ip" {
  value       = "10.0.2.10"
  description = "node_web internal IP"
}

output "db_ip" {
  value       = "10.0.2.20"
  description = "node_db internal IP (postgres:16-alpine, port 5432, db=labdb user=labuser)"
}
