substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  kind         = "oci"
  source       = "alpine:3.19"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "web" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link "dmz" {
    network = sysbox_network.dmz.id
    ip      = "10.0.1.10/24"
  }
}

resource "sysbox_node" "client" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link "dmz" {
    network = sysbox_network.dmz.id
    ip      = "10.0.1.20/24"
  }
}

resource "sysbox_actor" "red" {
  position = "internal"
  node     = sysbox_node.client.id
  command  = ["opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"]
  port     = 4096

  depends_on = ["sysbox_node.client"]
}

