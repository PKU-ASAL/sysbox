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
//	    program = "ip addr add 10.0.2.20/24 dev eth0 && ip link set eth0 up"
//	    shell   = "linux"
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

// New creates a libvirt substrate. Registration happens explicitly in
// cmd/sysbox/main.go alongside the other substrates.
func New() *Substrate {
	return &Substrate{}
}

func (s *Substrate) Name() string { return subName }

func (s *Substrate) PreflightChecks(required bool) []substrate.PreflightCheck {
	return substrate.LibvirtPreflightChecks(required)
}

// Capabilities: NICs are declared in the domain XML before StartNode
// (NICHotPlug=false); provisioners reach the VM over SSH.
func (s *Substrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		NICHotPlug:            false,
		NICKinds:              []string{"tap"},
		ConsoleKinds:          []string{"serial"},
		SupportsPause:         true, // virsh suspend/resume
		PortExposures:         []string{substrate.PortExposureNone, substrate.PortExposureDirect},
		GuestNetworkInitModes: []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit, substrate.GuestNetworkInitPreconfigured},
	}
}

// compile-time interface guard

// Validate rejects specs that cannot run on the libvirt substrate.
func (s *Substrate) Validate(spec substrate.NodeSpec) error {
	// Image validation: libvirt requires a QCow2 image. At plan time we
	// can't see ImageSpec fields (NodeSpec only has ImageRef); the actual
	// check happens in PrepareImage. Here we reject obviously wrong configs.
	if pc, ok := spec.ProviderConfig.(*Config); ok {
		if !supportedNetworkInitMode(pc.NetworkInit) {
			return substrate.NewValidationError("libvirt: explicit network_init must be %q or %q", substrate.GuestNetworkInitCloudInit, substrate.GuestNetworkInitPreconfigured)
		}
		if pc.SSHUser == "" {
			return substrate.NewValidationError("libvirt: ssh_user is required (VMs need SSH for provisioners)")
		}
	}
	return nil
}

// Dependencies returns empty deps — the libvirt Config only references
// the QCow2 image path directly (no cross-resource HCL refs like
// sysbox_kernel). If future Config fields reference other resources,
// override this method to declare them.
func (s *Substrate) Dependencies(_ any) substrate.ProviderDeps {
	return substrate.ProviderDeps{}
}
