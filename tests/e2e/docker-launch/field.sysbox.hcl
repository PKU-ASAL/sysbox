substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "app" {
  cidr = "172.31.91.0/24"
  nat  = true
}

resource "sysbox_image" "service" {
  substrate    = substrate.docker.local
  kind         = "oci"
  source       = "sysbox-launch-override:test"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "service" {
  substrate = substrate.docker.local
  image     = sysbox_image.service.id

  link "app" {
    network = sysbox_network.app.id
    ip      = "172.31.91.10/24"
  }

  provider "docker" {
    command = ["echo override > /tmp/launch-mode; exec sleep infinity"]
  }
}
