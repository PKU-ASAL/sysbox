# ── MicroVM smoke test: single NAT node ──────────────────────────────────────
#
# Uses Docker NAT networks only (no isolated netns/bridge), suitable for
# quick verification of the firecracker substrate + vm-vsock monitor.
#
# Prerequisites:
#   - firecracker binary in PATH
#   - mkfs.ext4 + losetup (sysbox-init builds an ext4 config drive)
#   - /tmp/fc-rootfs-user.ext4 (or change rootfs below; see microvm/README.md)
#
# The kernel is fetched on demand from firecracker-ci into
# ~/.cache/sysbox/artifacts/ and reused across runs.
#
# Usage:
#   sudo -E ./bin/sysbox apply -f examples/microvm/smoke.hcl --auto-approve
#   sudo ./bin/sysbox sensor start

substrate "firecracker" {
  alias = "fc"
}

resource "sysbox_kernel" "fc_510" {
  substrate = substrate.firecracker.fc
  source    = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245"
}

resource "sysbox_image" "alpine_vm" {
  substrate = substrate.firecracker.fc
  rootfs    = "/tmp/fc-rootfs-user.ext4"
}

resource "sysbox_network" "net_uplink" {
  cidr = "172.21.0.0/24"
  nat  = true
}

resource "sysbox_node" "vm_attack" {
  substrate = substrate.firecracker.fc
  image     = sysbox_image.alpine_vm.id
  kernel    = sysbox_kernel.fc_510.id
  vcpus     = 2
  memory    = "512"

  link {
    network = sysbox_network.net_uplink.id
    ip      = "172.21.0.10/24"
  }

  ssh_user = "root"
  ssh_pass = "root"

  provisioner "exec" {
    inline = ["uname -a"]
  }
}

resource "sysbox_monitor" "lab" {
  backend = "vm-vsock"
  nodes   = [sysbox_node.vm_attack.id]
  events  = ["execve", "connect", "openat"]

  extra = {
    agent_bin  = "/usr/local/bin/vm-sensor"
    event_file = "/tmp/vm-sensor-events.jsonl"
    vsock_port = "8900"
  }
}

output "vm_ip" {
  value       = "172.21.0.10"
  description = "NAT IP of the attacker VM"
}
