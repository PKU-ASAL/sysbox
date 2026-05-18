// Package libvirt implements the sysbox substrate interface for KVM/QEMU
// virtual machines managed via virsh. The substrate shells out to virsh (and
// qemu-img) — no CGO dependency required; only libvirtd must be running.
//
// # Prerequisites
//
//   - libvirtd running (systemctl start libvirtd)
//   - virsh + qemu-img in PATH
//   - caller in the libvirt group, or running as root
//
// # HCL usage
//
//	substrate "libvirt" { alias = "kvm" }
//
//	resource "sysbox_image" "ubuntu" {
//	  substrate = substrate.libvirt.kvm
//	  qcow2     = "/srv/images/ubuntu-22.04.qcow2"
//	}
//
//	resource "sysbox_node" "db" {
//	  substrate = substrate.libvirt.kvm
//	  image     = sysbox_image.ubuntu.id
//
//	  provider "libvirt" {
//	    vcpus   = 2
//	    memory  = "2048"
//	    ssh_user = "ubuntu"
//	    ssh_pass = "ubuntu"
//	  }
//
//	  link { network = sysbox_network.internal.id; ip = "10.0.2.20/24" }
//
//	  provisioner "exec" {
//	    inline = [
//	      "ip addr add 10.0.2.20/24 dev eth0",
//	      "ip link set eth0 up",
//	    ]
//	  }
//	}
package libvirt

import (
	"github.com/oslab/sysbox/pkg/substrate"
)

const subName = "libvirt"

// Substrate is the libvirt/KVM substrate implementation.
// It embeds BaseSubstrate so only the methods with libvirt-specific
// behaviour need to be declared explicitly.
type Substrate struct {
	substrate.BaseSubstrate
}

func init() {
	substrate.Register(&Substrate{})
}

func (s *Substrate) Name() string { return subName }

// Capabilities: NICs are declared in the domain XML before StartNode
// (NICHotPlug=false); provisioners reach the VM over SSH.
func (s *Substrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		NICHotPlug:   false,
		NICKinds:     []string{"tap"},
		ConsoleKinds: []string{"serial"},
	}
}

// compile-time interface guard
var _ substrate.Substrate = (*Substrate)(nil)
