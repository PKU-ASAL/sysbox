# ── MicroVM topology: three Firecracker nodes on isolated networks ─────────
#
# Three Firecracker microVM nodes on isolated networks.
#
# Prerequisites:
#   - firecracker binary in PATH
#   - mkfs.ext4 + losetup (for sysbox-init's config drive)
#   - SYSBOX_ROOTFS set, or default ~/.cache/sysbox/rootfs/ubuntu-24.04.ext4
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
# rootfs_path follows the same default as scripts/prepare-fc-rootfs.sh:
#   $SYSBOX_ROOTFS  (override)  →  $HOME/.cache/sysbox/rootfs/ubuntu-24.04.ext4
#
# When running with sudo, pass `sudo -E` so $HOME (and SYSBOX_ROOTFS if set)
# survive into the sysbox process; otherwise root's $HOME is used.

locals {
  rootfs_path = env("SYSBOX_ROOTFS") != "" ? env("SYSBOX_ROOTFS") : "${env("HOME")}/.cache/sysbox/rootfs/ubuntu-24.04.ext4"
}

# ── Kernel + Images ─────────────────────────────────────────────────────────

# Firecracker-team-maintained vmlinux with CONFIG_VSOCKETS=y and
# CONFIG_VIRTIO_VSOCKETS=y compiled in (required for sysbox-init's vsock
# provisioner channel). Cached at ~/.cache/sysbox/artifacts/<key>/ on
# first apply; reused on subsequent runs.
#
# See docs/firecracker-vmbox.md for the upstream "from zero" walkthrough
# that this URL came from.
resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245"
  # sha256  = "..." # set this to pin the build; verified on fetch and cache-hit
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc

  # Built by scripts/prepare-fc-rootfs.sh from the firecracker-ci official
  # ubuntu-24.04.squashfs. See docs/firecracker-vmbox.md.
  # Override with SYSBOX_ROOTFS env var when running sysbox apply.
  rootfs = local.rootfs_path

  # Alternatively, point at any other ext4 image; sysbox-init makes no
  # assumptions about the distro inside.
  # rootfs = "https://example.com/your-rootfs.ext4"
  # sha256 = "..."
}

resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  docker_ref = "alpine:latest"
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

  link {
    network = sysbox_network.net_dmz.id
    ip      = "10.0.11.10/24"
    gw      = "10.0.11.254"
  }

  link {
    network = sysbox_network.net_uplink_fc.id
    ip      = "172.22.0.10/24"
  }

  # ubuntu-24.04 rootfs has apt; use bash for `|| true` etc.
  provisioner "exec" {
    inline = ["uname -a", "ip -4 addr show eth0 | head -3"]
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

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.12.10/24"
    gw      = "10.0.12.254"
  }

  provisioner "exec" {
    inline = ["uname -a", "hostname"]
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

  link {
    network = sysbox_network.net_internal.id
    ip      = "10.0.12.20/24"
    gw      = "10.0.12.254"
  }

  provisioner "exec" {
    inline = ["uname -a", "cat /etc/os-release | head -3"]
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
