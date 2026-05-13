# Three-node attack lab — sysbox Phase 3 reference topology.
#
# Attack scenario: External attacker → DMZ web server → Internal DB
#
#   [host/agent]
#     └─ docker exec ──▶  node_attack  (10.0.1.10)   # attacker jump box
#                              │
#                        net_dmz  (10.0.1.0/24)
#                              │
#                        router  (10.0.1.254 / 10.0.2.254)
#                              │
#                       net_internal  (10.0.2.0/24)
#                        ├── node_web  (10.0.2.10)   # nginx web server
#                        └── node_db   (10.0.2.20)   # mock database (postgres port)
#
# Expected attack path for RL agent:
#   Step 1  nmap 10.0.2.0/24                  (recon)     T1595.001
#   Step 2  ssh root@10.0.2.10                (initial)   T1021.004
#   Step 3  curl http://10.0.2.20:5432/       (pivot)     T1105
#   Step 4  cat /etc/passwd                   (exfil)     T1003

substrate "docker" {
  alias = "light"
}

# ── Networks ─────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_network" "net_internal" {
  cidr = "10.0.2.0/24"
}

# ── Images ───────────────────────────────────────────────────────────────────

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  docker_ref = "alpine:latest"
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.light
  docker_ref = "nginx:alpine"
}

# Pre-built attacker image with nmap/ssh/curl installed at build time.
# Build with: docker build -t sysbox-attacker:latest -f Dockerfile.attacker .
# (Containers run with --network none, so tools must be baked into the image.)
resource "sysbox_image" "attacker" {
  substrate  = substrate.docker.light
  docker_ref = "sysbox-attacker:latest"
}

# ── Router (bridges DMZ ↔ internal) ──────────────────────────────────────────

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
}

# ── Nodes ─────────────────────────────────────────────────────────────────────

# node_attack: the attacker jump box.
# Uses the pre-built attacker image (nmap, ssh, curl already installed).
# Claude Code agent runs commands via `docker exec sysbox-node_attack`.
resource "sysbox_node" "node_attack" {
  image     = sysbox_image.attacker.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
    gw      = "10.0.1.254"
  }
}

# node_web: nginx web server in the DMZ-facing internal zone.
# Reachable from node_attack via the router.
resource "sysbox_node" "node_web" {
  image     = sysbox_image.nginx.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.10/24"
    gw      = "10.0.2.254"
  }
}

# node_db: mock database. Only reachable from net_internal.
# Runs a netcat listener on port 5432 to simulate postgres.
resource "sysbox_node" "node_db" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.20/24"
    gw      = "10.0.2.254"
  }
}

# Note: sysbox_ssh_access is removed.
# Containers run with --network none and cannot install packages at runtime.
# SSH tools are pre-baked into sysbox_image.attacker (Dockerfile.attacker).
# Inter-node SSH (node_attack → node_web) is set up by lab.sh after apply:
#   docker exec sysbox-node_attack sh -c "echo '<pubkey>' >> /root/.ssh/authorized_keys"
#   docker exec sysbox-node_web /usr/sbin/sshd
