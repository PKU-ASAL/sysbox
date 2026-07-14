# IPv4 controlled-egress reference topology.
# The router owns atomic NAT and firewall nftables tables. Forwarding is denied
# by default; internal workloads may open TCP/443 connections through uplink.

substrate "docker" {
  alias = "local"
}

resource "sysbox_image" "router" {
  substrate  = substrate.docker.local
  kind         = "oci"
  source       = "alpine:latest"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_network" "internal" {
  cidr = "10.42.0.0/24"
}

resource "sysbox_network" "uplink" {
  cidr = "172.31.42.0/24"
  nat  = true
}

resource "sysbox_router" "edge" {
  substrate = substrate.docker.local
  image     = sysbox_image.router.id

  interface "internal" {
    network = sysbox_network.internal.id
    ip      = "10.42.0.1/24"
  }

  interface "uplink" {
    network = sysbox_network.uplink.id
    ip      = "172.31.42.2/24"
  }

  nat_from = "internal"
  nat_to   = "uplink"
}

resource "sysbox_firewall" "egress" {
  attach_to       = sysbox_router.edge.id
  family          = "ipv4"
  default_input   = "accept"
  default_output  = "accept"
  default_forward = "drop"

  rule "allow_https" {
    direction         = "forward"
    protocol          = "tcp"
    source_cidrs      = ["10.42.0.0/24"]
    destination_ports = ["443"]
    input_attachment  = "internal"
    output_attachment = "uplink"
    states            = ["new"]
    verdict           = "accept"
    counter           = true
  }

  rule "allow_return" {
    direction         = "forward"
    protocol          = "all"
    input_attachment  = "uplink"
    output_attachment = "internal"
    states            = ["established", "related"]
    verdict           = "accept"
    counter           = true
  }
}
