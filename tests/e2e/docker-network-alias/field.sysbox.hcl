substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "app" {
  cidr = "172.31.92.0/24"
  nat  = true
}

resource "sysbox_image" "alpine" {
  substrate    = substrate.docker.local
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "mongo" {
  substrate = substrate.docker.local
  image     = sysbox_image.alpine.id

  link "app" {
    network = sysbox_network.app.id
    ip      = "172.31.92.10/24"
    aliases = ["database"]
  }

  provider "docker" {
    command = ["sleep", "infinity"]
  }
}

resource "sysbox_node" "target" {
  substrate = substrate.docker.local
  image     = sysbox_image.alpine.id

  link "app" {
    network = sysbox_network.app.id
    ip      = "172.31.92.11/24"
  }

  provider "docker" {
    command = ["sleep", "infinity"]
  }
}

resource "sysbox_node" "attacker" {
  substrate = substrate.docker.local
  image     = sysbox_image.alpine.id

  link "app" {
    network = sysbox_network.app.id
    ip      = "172.31.92.12/24"
  }

  provider "docker" {
    command = ["sleep", "infinity"]
  }
}
