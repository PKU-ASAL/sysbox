substrate "docker" {
  alias = "local"
}

resource "sysbox_network" "app" {
  cidr = "172.31.10.0/24"
  nat  = true
}

resource "sysbox_image" "nginx" {
  substrate  = substrate.docker.local
  kind         = "oci"
  source       = "nginx:alpine"
  architecture = "amd64"
  guest_family = "linux"
}

resource "sysbox_node" "web" {
  substrate = substrate.docker.local
  image     = sysbox_image.nginx.id

  link "app" {
    network = sysbox_network.app.id
    ip      = "172.31.10.10/24"
  }

  port {
    name      = "http"
    target    = 80
    published = 18080
    protocol  = "http"
    exposure  = "host"
    host_ip   = "127.0.0.1"
  }
}

output "web_ip" {
  value = "172.31.10.10"
}

output "web_url" {
  value = "http://127.0.0.1:18080"
}
