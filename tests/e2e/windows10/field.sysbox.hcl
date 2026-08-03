# Experimental Windows 10 domain-start lifecycle topology.
#
# This intentionally declares no links, provisioners, or connection block:
# the current libvirt provider can manage the VM lifecycle, but Windows guest
# networking and WinRM execution are not implemented yet.

substrate "libvirt" {
  alias = "local"
}

resource "sysbox_image" "windows10" {
  substrate   = substrate.libvirt.local
  kind         = "qcow2"
  source       = env("SYSBOX_WINDOWS10_QCOW2")
  architecture = "amd64"
  guest_family = "windows"
}

resource "sysbox_node" "windows10" {
  substrate = substrate.libvirt.local
  image     = sysbox_image.windows10.id
  vcpus     = 2
  memory    = "4096"

  provider "libvirt" {
    network_init = "preconfigured"
    vcpus         = 2
    memory        = "4096"
  }
}
