package libvirt

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// domainXML is the libvirt domain definition.
type domainXML struct {
	XMLName xml.Name   `xml:"domain"`
	Type    string     `xml:"type,attr"`
	Name    string     `xml:"name"`
	Memory  domMemory  `xml:"memory"`
	VCPU    int        `xml:"vcpu"`
	OS      domOS      `xml:"os"`
	CPU     domCPU     `xml:"cpu"`
	Devices domDevices `xml:"devices"`
	OnCrash string     `xml:"on_crash"`
}

type domMemory struct {
	Unit  string `xml:"unit,attr"`
	Value int    `xml:"chardata"`
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

type domDevices struct {
	Emulator   string      `xml:"emulator,omitempty"`
	Disks      []domDisk   `xml:"disk"`
	Interfaces []domIface  `xml:"interface"`
	Console    *domConsole `xml:"console,omitempty"`
	Serial     *domSerial  `xml:"serial,omitempty"`
	Channel    *domChannel `xml:"channel,omitempty"`
}

type domDisk struct {
	Type   string     `xml:"type,attr"`
	Device string     `xml:"device,attr"`
	Driver domDiskDrv `xml:"driver"`
	Source domDiskSrc `xml:"source"`
	Target domDiskTgt `xml:"target"`
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
	Name        string
	VCPUs       int
	MemoryMiB   int
	MachineType string
	DiskPath    string // absolute path to the per-VM qcow2 copy
	Bridges     []BridgeAttach
}

// BridgeAttach describes one network interface attached to a host bridge.
type BridgeAttach struct {
	Bridge string
	MAC    string // empty → libvirt auto-generates
}

// GenerateDomainXML produces the virsh-compatible domain XML for the given spec.
func GenerateDomainXML(spec DomainSpec) (string, error) {
	ifaces := make([]domIface, len(spec.Bridges))
	for i, b := range spec.Bridges {
		iface := domIface{
			Type:   "bridge",
			Source: domIfaceSrc{Bridge: b.Bridge},
			Model:  domIfaceMod{Type: "virtio"},
		}
		if b.MAC != "" {
			iface.MAC = &domIfaceMAC{Address: b.MAC}
		}
		ifaces[i] = iface
	}

	machine := spec.MachineType
	if machine == "" {
		machine = "q35"
	}

	d := domainXML{
		Type: "kvm",
		Name: spec.Name,
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
		CPU: domCPU{Mode: "host-passthrough"},
		Devices: domDevices{
			Emulator: "/usr/bin/qemu-system-x86_64",
			Disks: []domDisk{
				{
					Type:   "file",
					Device: "disk",
					Driver: domDiskDrv{Name: "qemu", Type: "qcow2"},
					Source: domDiskSrc{File: spec.DiskPath},
					Target: domDiskTgt{Dev: "vda", Bus: "virtio"},
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

	out, err := xml.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal domain XML: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
