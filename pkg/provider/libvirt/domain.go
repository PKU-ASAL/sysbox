package libvirt

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/oslab/sysbox/pkg/provider/network"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

// domainXML is the libvirt domain definition.
type domainXML struct {
	XMLName  xml.Name     `xml:"domain"`
	Type     string       `xml:"type,attr"`
	Name     string       `xml:"name"`
	UUID     string       `xml:"uuid,omitempty"`
	Metadata *domMetadata `xml:"metadata,omitempty"`
	Memory   domMemory    `xml:"memory"`
	VCPU     int          `xml:"vcpu"`
	OS       domOS        `xml:"os"`
	Features domFeatures  `xml:"features"`
	CPU      domCPU       `xml:"cpu"`
	GenID    *struct{}    `xml:"genid,omitempty"`
	Devices  domDevices   `xml:"devices"`
	OnCrash  string       `xml:"on_crash"`
}

type domMetadata struct {
	Managed *domManaged `xml:"urn:sysbox:managed managed"`
}
type domManaged struct {
	OwnerUID    string `xml:"owner_uid,attr"`
	ResourceUID string `xml:"resource_uid,attr"`
	TopologyUID string `xml:"topology_uid,attr"`
	Generation  int64  `xml:"generation,attr"`
	ActionID    string `xml:"action_id,attr"`
	State       string `xml:"state"`
}

type domMemory struct {
	Unit  string `xml:"unit,attr"`
	Value int    `xml:",chardata"`
}

type domOS struct {
	Type domOSType `xml:"type"`
	Boot domBoot   `xml:"boot"`
}

type domOSType struct {
	Arch    string `xml:"arch,attr,omitempty"`
	Machine string `xml:"machine,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type domBoot struct {
	Dev string `xml:"dev,attr"`
}

type domCPU struct {
	Mode string `xml:"mode,attr,omitempty"`
}

type domFeatures struct {
	ACPI struct{} `xml:"acpi"`
	APIC struct{} `xml:"apic"`
	PAE  struct{} `xml:"pae"`
}

type domDevices struct {
	Emulator   string       `xml:"emulator,omitempty"`
	Disks      []domDisk    `xml:"disk"`
	Interfaces []domIface   `xml:"interface"`
	Console    *domConsole  `xml:"console,omitempty"`
	Serial     *domSerial   `xml:"serial,omitempty"`
	Channel    *domChannel  `xml:"channel,omitempty"`
	Graphics   *domGraphics `xml:"graphics,omitempty"`
	Video      *domVideo    `xml:"video,omitempty"`
}

type domGraphics struct {
	Type     string `xml:"type,attr"`
	Autoport string `xml:"autoport,attr"`
	Listen   string `xml:"listen,attr"`
}

type domVideo struct {
	Model domVideoModel `xml:"model"`
}
type domVideoModel struct {
	Type string `xml:"type,attr"`
}

type domDisk struct {
	Type     string     `xml:"type,attr"`
	Device   string     `xml:"device,attr"`
	Driver   domDiskDrv `xml:"driver"`
	Source   domDiskSrc `xml:"source"`
	Target   domDiskTgt `xml:"target"`
	ReadOnly *struct{}  `xml:"readonly,omitempty"`
}

type domDiskDrv struct {
	Name string `xml:"name,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type domDiskSrc struct {
	File string `xml:"file,attr,omitempty"`
}

type domDiskTgt struct {
	Dev string `xml:"dev,attr"`
	Bus string `xml:"bus,attr,omitempty"`
}

type domIface struct {
	Type   string       `xml:"type,attr"`
	Source domIfaceSrc  `xml:"source"`
	Model  domIfaceMod  `xml:"model"`
	MAC    *domIfaceMAC `xml:"mac,omitempty"`
}

type domIfaceSrc struct {
	Bridge  string `xml:"bridge,attr,omitempty"`
	Network string `xml:"network,attr,omitempty"`
}

type domIfaceMod struct {
	Type string `xml:"type,attr"`
}

type domIfaceMAC struct {
	Address string `xml:"address,attr"`
}

type domConsole struct {
	Type   string        `xml:"type,attr"`
	Target domConsoleTgt `xml:"target"`
}

type domConsoleTgt struct {
	Type string `xml:"type,attr"`
	Port string `xml:"port,attr"`
}

type domSerial struct {
	Type string `xml:"type,attr"`
}

type domChannel struct {
	Type   string        `xml:"type,attr"`
	Target domChannelTgt `xml:"target"`
}

type domChannelTgt struct {
	Type string `xml:"type,attr"`
	Name string `xml:"name,attr"`
}

// DomainSpec holds everything needed to generate a domain XML.
type DomainSpec struct {
	Identity     *substrate.OperationIdentity
	ManagedState *HandleState
	Name         string
	UUID         string
	VCPUs        int
	MemoryMiB    int
	MachineType  string
	DiskPath     string // absolute path to the per-VM qcow2 copy
	SeedISO      string
	Bridges      []BridgeAttach
	GuestFamily  substrate.GuestFamily
}

// BridgeAttach describes one network interface attached to a host bridge.
type BridgeAttach struct {
	Target     network.BridgeTarget
	Name       string
	Netns      string
	Bridge     string
	MAC        string // empty → libvirt auto-generates
	IPPrefixes []string
	Gateway    string
}

// guestFamilyFromDomainXML recognizes the explicit markers emitted for Windows
// guests. Linux domains keep the legacy SSH path; an unmarked imported domain
// is handled conservatively by ReadNode as an unknown/SSH guest.
func guestFamilyFromDomainXML(domain domainXML) substrate.GuestFamily {
	if domain.GenID != nil {
		return substrate.GuestFamilyWindows
	}
	if channel := domain.Devices.Channel; channel != nil && channel.Target.Name == "org.qemu.guest_agent.0" {
		return substrate.GuestFamilyWindows
	}
	return substrate.GuestFamilyLinux
}

// GenerateDomainXML produces the virsh-compatible domain XML for the given spec.
func GenerateDomainXML(spec DomainSpec) (string, error) {
	windows := spec.GuestFamily == substrate.GuestFamilyWindows
	nicModel, diskBus, diskDev := "virtio", "virtio", "vda"
	if windows {
		nicModel, diskBus, diskDev = "e1000e", "sata", "sda"
	}
	ifaces := make([]domIface, 0, len(spec.Bridges))
	for _, b := range spec.Bridges {
		iface := domIface{
			Type:   "bridge",
			Source: domIfaceSrc{Bridge: b.Bridge},
			Model:  domIfaceMod{Type: nicModel},
		}
		if b.MAC != "" {
			iface.MAC = &domIfaceMAC{Address: b.MAC}
		}
		ifaces = append(ifaces, iface)
	}

	machine := spec.MachineType
	if machine == "" {
		machine = "q35"
	}

	d := domainXML{
		Type: "kvm",
		Name: spec.Name,
		UUID: spec.UUID,
		Memory: domMemory{
			Unit:  "MiB",
			Value: spec.MemoryMiB,
		},
		VCPU: spec.VCPUs,
		OS: domOS{
			Type: domOSType{
				Arch:    "x86_64",
				Machine: machine,
				Value:   "hvm",
			},
			Boot: domBoot{Dev: "hd"},
		},
		Features: domFeatures{},
		CPU:      domCPU{Mode: "host-passthrough"},
		Devices: domDevices{
			Emulator: "/usr/bin/qemu-system-x86_64",
			Disks: []domDisk{
				{
					Type:   "file",
					Device: "disk",
					Driver: domDiskDrv{Name: "qemu", Type: "qcow2"},
					Source: domDiskSrc{File: spec.DiskPath},
					Target: domDiskTgt{Dev: diskDev, Bus: diskBus},
				},
			},
			Interfaces: ifaces,
			Console: &domConsole{
				Type:   "pty",
				Target: domConsoleTgt{Type: "serial", Port: "0"},
			},
			Serial: &domSerial{Type: "pty"},
		},
		OnCrash: "destroy",
	}
	if windows {
		d.GenID = &struct{}{}
		d.Devices.Channel = &domChannel{Type: "unix", Target: domChannelTgt{Type: "virtio", Name: "org.qemu.guest_agent.0"}}
		d.Devices.Graphics = &domGraphics{Type: "vnc", Autoport: "yes", Listen: "127.0.0.1"}
		d.Devices.Video = &domVideo{Model: domVideoModel{Type: "vga"}}
	}
	if spec.SeedISO != "" {
		d.Devices.Disks = append(d.Devices.Disks, domDisk{Type: "file", Device: "cdrom", Driver: domDiskDrv{Name: "qemu", Type: "raw"}, Source: domDiskSrc{File: spec.SeedISO}, Target: domDiskTgt{Dev: "sda", Bus: "sata"}, ReadOnly: &struct{}{}})
	}
	if spec.Identity != nil {
		if err := spec.Identity.Validate(); err != nil {
			return "", err
		}
		if spec.ManagedState == nil || spec.UUID == "" {
			return "", fmt.Errorf("managed domain requires durable state and UUID")
		}
		state, err := json.Marshal(spec.ManagedState)
		if err != nil {
			return "", err
		}
		id := spec.Identity
		d.Metadata = &domMetadata{Managed: &domManaged{OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, TopologyUID: id.TopologyUID, Generation: id.Generation, ActionID: id.ActionID, State: string(state)}}
	}

	out, err := xml.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal domain XML: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
