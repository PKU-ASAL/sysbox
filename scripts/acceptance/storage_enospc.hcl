resource "sysbox_image" "base" {
  substrate = "docker"
  source = "alpine:3.22"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}
resource "sysbox_node" "node" {
  substrate = "docker"
  image = sysbox_image.base.id
  provider "docker" {
    entrypoint = ["sleep"]
    command = ["infinity"]
  }
}
