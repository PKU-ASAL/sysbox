substrate "docker" {
  alias = "local"
}

substrate "firecracker" {
  alias = "local"
}

substrate "libvirt" {
  alias = "local"
}

resource "sysbox_kernel" "linux" {
  substrate = substrate.firecracker.local
  source    = env("SYSBOX_KERNEL")
}

resource "sysbox_image" "docker" {
  substrate  = substrate.docker.local
  docker_ref = "alpine:latest"
}

resource "sysbox_image" "firecracker" {
  substrate = substrate.firecracker.local
  rootfs    = env("SYSBOX_ROOTFS")
}

resource "sysbox_image" "libvirt" {
  substrate = substrate.libvirt.local
  qcow2     = env("SYSBOX_QCOW2")
}

resource "sysbox_network" "matrix" {
  cidr = "10.44.0.0/24"
}

resource "sysbox_node" "docker" {
  substrate = substrate.docker.local
  image     = sysbox_image.docker.id

  link "matrix" {
    network = sysbox_network.matrix.id
    ip      = "10.44.0.10/24"
  }
}

resource "sysbox_node" "firecracker" {
  substrate = substrate.firecracker.local
  image     = sysbox_image.firecracker.id
  vcpus     = 1
  memory    = "256"

  provider "firecracker" {
    kernel = sysbox_kernel.linux.id
  }

  link "matrix" {
    network = sysbox_network.matrix.id
    ip      = "10.44.0.20/24"
  }
}

resource "sysbox_node" "libvirt" {
  substrate = substrate.libvirt.local
  image     = sysbox_image.libvirt.id
  vcpus     = 1
  memory    = "2048"

  provider "libvirt" {
    vcpus             = 1
    memory            = "1024"
    network_init       = "cloud_init"
    ssh_user           = "sysbox"
    ssh_key            = env("SYSBOX_MATRIX_SSH_PRIVATE_KEY")
    ssh_authorized_key = env("SYSBOX_MATRIX_SSH_PUBLIC_KEY")
  }

  link "matrix" {
    network = sysbox_network.matrix.id
    ip      = "10.44.0.30/24"
  }
}

output "docker_ip" {
  value = "10.44.0.10"
}

output "firecracker_ip" {
  value = "10.44.0.20"
}

output "libvirt_ip" {
  value = "10.44.0.30"
}
