# ── Mixed topology: Docker + Firecracker on shared networks ──────────────────
#
# Mixed topology: Docker containers + Firecracker microVMs on shared networks.
# Docker nodes use veth pairs, VM nodes use TAP devices — both attach to the
# same Linux bridge so they share the same L2 domain.
#
# Prerequisites:
#   - firecracker binary in PATH
#   - SYSBOX_ROOTFS set, or default ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4
#   - sysbox-attacker:latest image (built by lab.sh)
#
# Usage:
#   sudo -E make lab SUITE=mixed

# ── Substrates ──────────────────────────────────────────────────────────────

substrate "docker" {
  alias = "dk"
}

substrate "firecracker" {
  alias = "fc"
}

# ── Locals ──────────────────────────────────────────────────────────────────

locals {
  rootfs_path = env("SYSBOX_ROOTFS") != "" ? env("SYSBOX_ROOTFS") : "${env("HOME")}/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
}

# ── Kernel + Images ─────────────────────────────────────────────────────────

# Same firecracker-ci kernel used in the microvm example. Includes
# CONFIG_VSOCKETS=y and CONFIG_VIRTIO_VSOCKETS=y (required for vsock-rpc).
resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245"
}

resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  docker_ref = "alpine:latest"
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.dk
  docker_ref = "nginx:alpine"
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc
  rootfs    = local.rootfs_path
}

resource "sysbox_image" "attacker_docker" {
  substrate  = substrate.docker.dk
  docker_ref = "sysbox-attacker:latest"
}

# ── Networks ────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_network" "net_internal" {
  cidr = "10.0.2.0/24"
}

resource "sysbox_network" "net_uplink" {
  cidr = "172.20.0.0/24"
  nat  = true
}

# ── Router (Docker — needs iptables for NAT) ────────────────────────────────

resource "sysbox_router" "core" {
  substrate = substrate.docker.dk
  image     = sysbox_image.alpine_docker.id

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

# ── Docker nodes ─────────────────────────────────────────────────────────────

resource "sysbox_node" "node_attack" {
  substrate = substrate.docker.dk
  image     = sysbox_image.attacker_docker.id

  provider "docker" {
    privileged = true
    pid_mode   = "host"
  }

  link {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
  }

  link {
    network = sysbox_network.net_uplink.id
    ip      = "172.20.0.10/24"
  }
}

resource "sysbox_node" "node_web" {
  substrate = substrate.docker.dk
  image     = sysbox_image.nginx.id

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.10/24"
    gw      = "10.0.2.254"
  }

  depends_on = [
    "sysbox_router.core",
  ]
}

# ── Firecracker node (isolated kernel, vm-vsock observation) ────────────────

resource "sysbox_node" "node_db" {
  substrate = substrate.firecracker.fc
  image     = sysbox_image.alpine_vm.id
  vcpus     = 1
  memory    = "256"

  provider "firecracker" {
    kernel   = sysbox_kernel.fc_510.id
    ssh_user = "root"
    ssh_pass = "root"
  }

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.20/24"
    gw      = "10.0.2.254"
  }

  depends_on = [
    "sysbox_router.core",
  ]

  provisioner "exec" {
    inline = ["apt-get update -qq && apt-get install -y -qq postgresql 2>/dev/null || true"]
  }
}

# ── Actor (on Docker node — uses docker exec) ───────────────────────────────

resource "sysbox_actor" "red" {
  position = "internal"
  node     = sysbox_node.node_attack.id
  command  = ["opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"]
  port     = 4096
  env      = { DEEPSEEK_API_KEY = env("DEEPSEEK_API_KEY") }
}


# ── Outputs ─────────────────────────────────────────────────────────────────

output "attacker_acp" {
  value       = "http://172.20.0.10:4096"
  description = "ACP URL for the attacker agent"
}

output "vm_db_ip" {
  value       = "10.0.2.20"
  description = "IP of the database VM (Firecracker)"
}
