# Three-node attack lab — sysbox reference topology.
#
# Attack topology: node_attack → DMZ router → node_web (nginx) →
#                  node_db (postgres:16-alpine)
#
#   [host / scenario runner]
#        │
#        ▼
#   node_attack  (10.0.1.10 / 172.30.0.10)
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
  uplink_cidr   = "172.30.0.0/24"
  uplink_gw     = "172.30.0.1"
  router_dmz_ip = "10.0.1.254"
}

# ── Networks ──────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = local.dmz_cidr
}

resource "sysbox_network" "net_internal" {
  cidr = local.internal_cidr
}

# nat=true: Docker-managed bridge masquerading (owned by the Docker daemon).
# Gives node_attack internet access for LLM API calls.
# Also accessible from the host at 172.30.0.10 (episode runner connects here).
resource "sysbox_network" "net_uplink" {
  cidr = local.uplink_cidr
  nat  = true
}

# ── Images ────────────────────────────────────────────────────────────────────

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.light
  kind         = "oci"
  source       = "nginx:alpine"
  architecture = "amd64"
  guest_family = "linux"
}

# Pre-built attacker image with network and HTTP tools.
# Build with: docker build -t sysbox-attacker:latest -f Dockerfile.attacker .
resource "sysbox_image" "attacker" {
  substrate  = substrate.docker.light
  kind         = "oci"
  source       = "sysbox-attacker:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "postgres" {
  substrate  = substrate.docker.light
  kind         = "oci"
  source       = "postgres:16-alpine"
  architecture = "amd64"
  guest_family = "linux"
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
    ip      = "172.30.0.254/24"
  }

  nat_from = "internal"
  nat_to   = "uplink"
}

# ── Nodes ─────────────────────────────────────────────────────────────────────

resource "sysbox_node" "node_attack" {
  image     = sysbox_image.attacker.id
  substrate = substrate.docker.light

  # DMZ: lab-internal communication (to router and victim nodes).
  # No gw here — routing is handled by route {} blocks below.
  link "dmz" {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
  }

  # Uplink: internet access (LLM API) + reachable from host (episode runner).
  link "uplink" {
    network = sysbox_network.net_uplink.id
    ip      = "172.30.0.10/24"
  }

  # Declarative static routes (Terraform-style, replaces `ip route add` provisioners).
  route {
    dst = "10.0.2.0/24"
    via = "10.0.1.254"
  }
  route {
    dst = "0.0.0.0/0"
    via = "172.30.0.1"
  }

  # Copy host SSH pubkey into node so scenario tools can pivot to victim nodes.
  provisioner "file" {
    source      = env("LAB_SSH_PUBKEY")
    destination = "/tmp/host_pubkey"
  }

  provisioner "exec" {
    program = "mkdir -p /root/.ssh && cat /tmp/host_pubkey >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && rm /tmp/host_pubkey"
    shell   = "linux"
  }
}

resource "sysbox_node" "node_web" {
  image     = sysbox_image.nginx.id
  substrate = substrate.docker.light

  link "internal" {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.10/24"
    gw      = "10.0.2.254"
  }
}

resource "sysbox_node" "node_db" {
  image     = sysbox_image.postgres.id
  substrate = substrate.docker.light

  link "internal" {
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

# ── Outputs ───────────────────────────────────────────────────────────────────

output "attacker_lab_ip" {
  value       = "10.0.1.10"
  description = "node_attack DMZ IP (lab-internal)"
}

output "attacker_uplink_ip" {
  value       = "172.30.0.10"
  description = "node_attack uplink IP (reachable from host)"
}

output "web_ip" {
  value       = "10.0.2.10"
  description = "node_web internal IP"
}

output "db_ip" {
  value       = "10.0.2.20"
  description = "node_db internal IP (postgres:16-alpine, port 5432, db=labdb user=labuser)"
}
