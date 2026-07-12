# Two networks bridged by a router.
#
# Topology:
#
#   node_a (10.0.1.10) -- net_a (10.0.1.0/24) -- router.eth0 (10.0.1.254)
#                                                 |
#                                                 router.eth1 (10.0.2.254)
#                                                 |
#   node_b (10.0.2.20) -- net_b (10.0.2.0/24) ----+
#
# Each end-node uses the router as its default gateway. The router
# enables IPv4 forwarding so node_a can ping node_b across subnets.

substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "net_a" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_network" "net_b" {
  cidr = "10.0.2.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  docker_ref = "alpine:latest"
}

resource "sysbox_router" "edge" {
  substrate = substrate.docker.light
  image     = sysbox_image.alpine.id

  interface "lan" {
    network = sysbox_network.net_a.id
    ip      = "10.0.1.254/24"
  }

  interface "wan" {
    network = sysbox_network.net_b.id
    ip      = "10.0.2.254/24"
  }
}

resource "sysbox_node" "node_a" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link "net_a" {
    network = sysbox_network.net_a.id
    ip      = "10.0.1.10/24"
    gw      = "10.0.1.254"
  }
}

resource "sysbox_node" "node_b" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light

  link "net_b" {
    network = sysbox_network.net_b.id
    ip      = "10.0.2.20/24"
    gw      = "10.0.2.254"
  }
}
