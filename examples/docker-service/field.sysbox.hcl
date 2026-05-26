substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "app" {
  cidr = "172.31.10.0/24"
  nat  = true
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.local
  docker_ref = "nginx:alpine"
}

resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = sysbox_image.nginx.id

  link {
    network = sysbox_network.app.id
    ip      = "172.31.10.10/24"
  }
}

output "web_ip" {
  value = "172.31.10.10"
}
