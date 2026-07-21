# ── MicroVM topology: three Firecracker nodes on isolated networks ─────────
#
# Three Firecracker microVM nodes on isolated networks.
#
# Prerequisites:
#   - firecracker binary in PATH
#   - mkfs.ext4 + losetup (for sysbox-init's config drive)
#   - SYSBOX_ROOTFS set, or default $SYSBOX_CACHE/rootfs/ubuntu-24.04.ext4
#     when running in the API container, otherwise ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4
#
# Usage:
#   sudo -E make lab SUITE=microvm

# ── Substrates ──────────────────────────────────────────────────────────────

substrate "firecracker" {
  alias = "fc"
}

substrate "docker" {
  alias = "dk"
}

# ── Locals ──────────────────────────────────────────────────────────────────
#
# rootfs_path follows the same default as scripts/prepare-fc-rootfs.sh for
# local CLI usage, while API/docker-compose can use the mounted SYSBOX_CACHE:
#   $SYSBOX_ROOTFS  (override)
#   $SYSBOX_CACHE/rootfs/ubuntu-24.04.ext4
#   $HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4
#
# When running with sudo, pass `sudo -E` so $HOME (and SYSBOX_ROOTFS if set)
# survive into the sysbox process; otherwise root's $HOME is used.

locals {
  rootfs_path = env_optional("SYSBOX_ROOTFS") != "" ? env_optional("SYSBOX_ROOTFS") : (env_optional("SYSBOX_CACHE") != "" ? "${env_optional("SYSBOX_CACHE")}/rootfs/ubuntu-24.04.ext4" : "${env_optional("HOME")}/.cache/sysbox/rootfs/ubuntu-24.04.ext4")
}

# ── Kernel + Images ─────────────────────────────────────────────────────────

# Firecracker-team-maintained vmlinux with CONFIG_VSOCKETS=y and
# CONFIG_VIRTIO_VSOCKETS=y compiled in (required for sysbox-init's vsock
# provisioner channel). Cached at ~/.cache/sysbox/artifacts/<key>/ on
# first apply; reused on subsequent runs.
#
# See docs/operations/artifacts.md for kernel/rootfs preparation
# that this URL came from.
resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245"
  architecture = "amd64"
  sha256    = "643096c1fabf0fbbda1d03c100b9e86b4e6965fd5a01d3c6fb9e8c0ecb7fbfc9"
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc

  # Built by scripts/prepare-fc-rootfs.sh from the firecracker-ci official
  # ubuntu-24.04.squashfs. See docs/operations/artifacts.md.
  # Override with SYSBOX_ROOTFS env var when running sysbox apply.
  kind         = "rootfs"
  source       = local.rootfs_path
  architecture = "amd64"
  guest_family = "linux"

  # Alternatively, point at any other ext4 image; sysbox-init makes no
  # assumptions about the distro inside.
  # rootfs = "https://example.com/your-rootfs.ext4"
  # sha256 = "..."
}

resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

# ── Networks ────────────────────────────────────────────────────────────────

resource "sysbox_network" "net_dmz" {
  cidr = "10.0.11.0/24"
}

resource "sysbox_network" "net_internal" {
  cidr = "10.0.12.0/24"
}

resource "sysbox_network" "net_uplink_fc" {
  cidr = "172.22.0.0/24"
  nat  = true
}

# ── Router ──────────────────────────────────────────────────────────────────

resource "sysbox_router" "core" {
  substrate = substrate.docker.dk
  image     = sysbox_image.alpine_docker.id

  interface "dmz" {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.11.254/24"
  }

  interface "internal" {
    network = sysbox_network.net_internal.id
    ip      = "10.0.12.254/24"
  }

  nat_from = "dmz"
  nat_to   = "internal"
}

# ── Nodes ────────────────────────────────────────────────────────────────────

resource "sysbox_node" "node_attack" {
  substrate = substrate.firecracker.fc
  image     = sysbox_image.alpine_vm.id
  vcpus     = 2
  memory    = "512"

  provider "firecracker" {
    kernel   = sysbox_kernel.fc_510.id
    ssh_user = "root"
    ssh_pass = "root"
  }

  link "dmz" {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.11.10/24"
    gw      = "10.0.11.254"
  }

  link "uplink" {
    network = sysbox_network.net_uplink_fc.id
    ip      = "172.22.0.10/24"
  }

  # Declarative static routes for cross-subnet access via router.
  route {
    dst = "10.0.12.0/24"
    via = "10.0.11.254"
  }

  # ubuntu-24.04 rootfs has apt; use bash for `|| true` etc.
  provisioner "exec" {
    program = "uname -a && ip -4 addr show eth0 | head -3"
    shell   = "linux"
  }
}

resource "sysbox_node" "node_web" {
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
    ip      = "10.0.12.10/24"
    gw      = "10.0.12.254"
  }

  provisioner "exec" {
    program = "uname -a && hostname"
    shell   = "linux"
  }
}

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
    ip      = "10.0.12.20/24"
    gw      = "10.0.12.254"
  }

  provisioner "exec" {
    program = "uname -a && cat /etc/os-release | head -3"
    shell   = "linux"
  }
}


# ── Outputs ─────────────────────────────────────────────────────────────────

output "attacker_ip" {
  value       = "10.0.11.10"
  description = "IP of the attacker VM"
}

output "uplink_ip" {
  value       = "172.22.0.10"
  description = "NAT IP of the attacker VM (reachable from host)"
}
