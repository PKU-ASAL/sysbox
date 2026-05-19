# ── libvirt-vm: KVM nodes on isolated sysbox networks ───────────────────────
#
# Two KVM/QEMU virtual machines sharing an isolated L2 segment.
# The first VM also gets a NAT uplink so the host can SSH into it.
#
# Prerequisites:
#   - libvirtd running: systemctl start libvirtd
#   - virsh + qemu-img in PATH
#   - Caller in the libvirt group (or run as root)
#   - A qcow2 base image: set SYSBOX_QCOW2 or use the default path
#   - The VM image must have sshd running with ssh_user/ssh_pass credentials
#
# Usage:
#   export SYSBOX_QCOW2=/srv/images/ubuntu-22.04.qcow2
#   sudo -E sysbox apply examples/libvirt-vm/field.sysbox.hcl

# ── Substrates ───────────────────────────────────────────────────────────────

substrate "libvirt" {
  alias = "kvm"
}

substrate "docker" {
  alias = "dk"
}

# ── Locals ───────────────────────────────────────────────────────────────────

locals {
  qcow2_path = env("SYSBOX_QCOW2") != "" ? env("SYSBOX_QCOW2") : "${env("HOME")}/.cache/sysbox/images/ubuntu-22.04.qcow2"
}

# ── Images ───────────────────────────────────────────────────────────────────

resource "sysbox_image" "ubuntu_kvm" {
  substrate = substrate.libvirt.kvm
  qcow2     = local.qcow2_path
}

resource "sysbox_image" "alpine_docker" {
  substrate  = substrate.docker.dk
  docker_ref = "alpine:latest"
}

# ── Networks ─────────────────────────────────────────────────────────────────

resource "sysbox_network" "internal" {
  cidr = "10.0.20.0/24"
}

resource "sysbox_network" "uplink" {
  cidr = "172.25.0.0/24"
  nat  = true
}

# ── Router ───────────────────────────────────────────────────────────────────

resource "sysbox_router" "gw" {
  substrate = substrate.docker.dk
  image     = sysbox_image.alpine_docker.id

  interface "internal" {
    network = sysbox_network.internal.id
    ip      = "10.0.20.254/24"
  }
}

# ── KVM Nodes ────────────────────────────────────────────────────────────────

resource "sysbox_node" "server" {
  substrate = substrate.libvirt.kvm
  image     = sysbox_image.ubuntu_kvm.id

  provider "libvirt" {
    vcpus    = 2
    memory   = "1024"
    ssh_user = "ubuntu"
    ssh_pass = "ubuntu"
  }

  # Link to the internal network (libvirt creates a TAP and attaches to bridge)
  link {
    network = sysbox_network.internal.id
    ip      = "10.0.20.10/24"
  }

  # Configure IP on the VM's first interface via a provisioner.
  # sysbox will SSH to ssh_ip (set below) to run these commands.
  # Static routes are declared via route {} blocks (Terraform-style).
  route {
    dst = "0.0.0.0/0"
    via = "10.0.20.254"
  }

  provisioner "exec" {
    inline = [
      "ip addr add 10.0.20.10/24 dev eth0 || true",
      "ip link set eth0 up",
    ]
  }
}

resource "sysbox_node" "client" {
  substrate = substrate.libvirt.kvm
  image     = sysbox_image.ubuntu_kvm.id

  provider "libvirt" {
    vcpus    = 1
    memory   = "512"
    ssh_user = "ubuntu"
    ssh_pass = "ubuntu"
  }

  link {
    network = sysbox_network.internal.id
    ip      = "10.0.20.20/24"
  }

  route {
    dst = "0.0.0.0/0"
    via = "10.0.20.254"
  }

  provisioner "exec" {
    inline = [
      "ip addr add 10.0.20.20/24 dev eth0 || true",
      "ip link set eth0 up",
    ]
  }
}

# ── Outputs ──────────────────────────────────────────────────────────────────

output "server_ip" {
  value       = "10.0.20.10"
  description = "Internal IP of the server VM"
}

output "client_ip" {
  value       = "10.0.20.20"
  description = "Internal IP of the client VM"
}
