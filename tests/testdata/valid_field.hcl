substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = "docker"
  docker_ref = "alpine:3.19"
}

resource "sysbox_node" "web" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"

  link {
    network = "sysbox_network.dmz.id"
    ip      = "10.0.1.10/24"
  }
}

resource "sysbox_node" "client" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"

  link {
    network = "sysbox_network.dmz.id"
    ip      = "10.0.1.20/24"
  }
}
