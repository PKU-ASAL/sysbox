variable "cidr_dmz" {
  default = "10.0.1.0/24"
}

variable "cidr_internal" {
  default = "10.0.2.0/24"
}

resource "sysbox_network" "dmz" {
  cidr = var.cidr_dmz
}

resource "sysbox_network" "internal" {
  cidr = var.cidr_internal
}

output "dmz_id" {
  value       = sysbox_network.dmz.id
  description = "ID of the DMZ network"
}

output "internal_id" {
  value       = sysbox_network.internal.id
  description = "ID of the internal network"
}
