substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "lan" {
  cidr = "10.0.99.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = "docker"
  docker_ref = "alpine:latest"
}

resource "sysbox_node" "node_a" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"

  link {
    network = "sysbox_network.lan.id"
    ip      = "10.0.99.10/24"
    gw      = "10.0.99.1"
  }
}

resource "sysbox_node" "node_b" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"

  link {
    network = "sysbox_network.lan.id"
    ip      = "10.0.99.20/24"
    gw      = "10.0.99.1"
  }
}
