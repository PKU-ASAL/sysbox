# ── Mixed topology: Docker + Firecracker on shared networks ──────────────────
#
# Mixed topology: Docker containers + Firecracker microVMs on shared networks.
# Docker nodes use veth pairs, VM nodes use TAP devices — both attach to the
# same Linux bridge so they share the same L2 domain.
#
# Prerequisites:
#   - firecracker binary in PATH
#   - SYSBOX_ROOTFS set, or default $SYSBOX_CACHE/rootfs/ubuntu-24.04.ext4
#     when running in the API container, otherwise ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4
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
  rootfs_path = env_optional("SYSBOX_ROOTFS") != "" ? env_optional("SYSBOX_ROOTFS") : (env_optional("SYSBOX_CACHE") != "" ? "${env_optional("SYSBOX_CACHE")}/rootfs/ubuntu-24.04.ext4" : "${env_optional("HOME")}/.cache/sysbox/rootfs/ubuntu-24.04.ext4")
}

# ── Kernel + Images ─────────────────────────────────────────────────────────

# Same firecracker-ci kernel used in the microvm example. Includes
# CONFIG_VSOCKETS=y and CONFIG_VIRTIO_VSOCKETS=y (required for vsock-rpc).
resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245"
  architecture = "amd64"
  sha256    = "643096c1fabf0fbbda1d03c100b9e86b4e6965fd5a01d3c6fb9e8c0ecb7fbfc9"
}

resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.dk
  kind         = "oci"
  source       = "nginx:alpine"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc
  kind         = "rootfs"
  source       = local.rootfs_path
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_image" "attacker_docker" {
  substrate  = substrate.docker.dk
  kind         = "oci"
  source       = "sysbox-attacker:latest"
  architecture = "amd64"
  guest_family = "linux"
}

# ── Networks ────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_network" "net_internal" {
  cidr = "10.0.2.0/24"
}

resource "sysbox_network" "net_uplink" {
  cidr = "172.30.1.0/24"
  nat  = true
}

# ── Router (Docker — topology-owned nftables NAT) ────────────────────────────────

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
    ip      = "172.30.1.254/24"
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

  link "dmz" {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.1.10/24"
  }

  link "uplink" {
    network = sysbox_network.net_uplink.id
    ip      = "172.30.1.10/24"
  }

  # Declarative static routes for cross-subnet access via router.
  route {
    dst = "10.0.2.0/24"
    via = "10.0.1.254"
  }
}

resource "sysbox_node" "node_web" {
  substrate = substrate.docker.dk
  image     = sysbox_image.nginx.id

  link "internal" {
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

  link "internal" {
    network = sysbox_network.net_internal.id
    ip      = "10.0.2.20/24"
    gw      = "10.0.2.254"
  }

  depends_on = [
    "sysbox_router.core",
  ]

  provisioner "exec" {
    program = "apt-get update -qq && apt-get install -y -qq postgresql 2>/dev/null || true"
    shell   = "linux"
  }
}

# ── Outputs ─────────────────────────────────────────────────────────────────

output "vm_db_ip" {
  value       = "10.0.2.20"
  description = "IP of the database VM (Firecracker)"
}
