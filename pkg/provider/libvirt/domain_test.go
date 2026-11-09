package libvirt

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestGenerateDomainXMLWindows(t *testing.T) {
	xml, err := GenerateDomainXML(DomainSpec{
		Name: "ad-dc", VCPUs: 4, MemoryMiB: 6144, DiskPath: "/tmp/windows.qcow2",
		GuestFamily: substrate.GuestFamilyWindows,
		Bridges:     []BridgeAttach{{Bridge: "sysbox-ad", MAC: "52:54:00:ad:00:10"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<genid>", `bus="sata"`, `type="e1000e"`, "org.qemu.guest_agent.0", `type="vnc"`} {
		if !strings.Contains(xml, want) {
			t.Errorf("Windows XML missing %q: %s", want, xml)
		}
	}
}

func TestGenerateDomainXML_Basic(t *testing.T) {
	xml, err := GenerateDomainXML(DomainSpec{
		Name:        "test-vm",
		VCPUs:       2,
		MemoryMiB:   512,
		MachineType: "q35",
		DiskPath:    "/tmp/disk.qcow2",
		SeedISO:     "/tmp/seed.iso",
		Bridges:     []BridgeAttach{{Bridge: "sysbox-br0"}},
	})
	if err != nil {
		t.Fatalf("GenerateDomainXML error: %v", err)
	}

	for _, want := range []string{
		"test-vm",
		"q35",
		"/tmp/disk.qcow2",
		"/tmp/seed.iso",
		"cdrom",
		"readonly",
		"<features>",
		"<acpi></acpi>",
		"<apic></apic>",
		"<pae></pae>",
		"sysbox-br0",
		"kvm",
		"virtio",
		"host-passthrough",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("domain XML missing %q\ngot:\n%s", want, xml)
		}
	}
}

func TestParseMiB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"512", 512},
		{"1024m", 1024},
		{"2G", 2048},
		{"4GiB", 4096},
		{"1GB", 1024},
	}
	for _, c := range cases {
		got, err := parseMiB(c.in)
		if err != nil {
			t.Errorf("parseMiB(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseMiB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGuestFamilyFromDomainXML(t *testing.T) {
	var windows domainXML
	if err := xml.Unmarshal([]byte(`<domain><genid/><devices><channel type="unix"><target type="virtio" name="org.qemu.guest_agent.0"/></channel></devices></domain>`), &windows); err != nil {
		t.Fatal(err)
	}
	if got := guestFamilyFromDomainXML(windows); got != substrate.GuestFamilyWindows {
		t.Fatalf("Windows domain detected as %q", got)
	}

	var linux domainXML
	if err := xml.Unmarshal([]byte(`<domain><devices><disk device="disk"><target bus="virtio" dev="vda"/></disk></devices></domain>`), &linux); err != nil {
		t.Fatal(err)
	}
	if got := guestFamilyFromDomainXML(linux); got != substrate.GuestFamilyLinux {
		t.Fatalf("Linux domain detected as %q", got)
	}
}

func TestGenerateDomainXMLWindowsSeedHasDistinctTargetsAndControllers(t *testing.T) {
	raw, err := GenerateDomainXML(DomainSpec{Name: "windows", VCPUs: 2, MemoryMiB: 2048, DiskPath: "/tmp/base.qcow2", SeedISO: "/tmp/seed.iso", GuestFamily: substrate.GuestFamilyWindows})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Devices struct {
			Disks []struct {
				Target struct {
					Dev string `xml:"dev,attr"`
				} `xml:"target"`
			} `xml:"disk"`
			Controllers []struct {
				Type string `xml:"type,attr"`
			} `xml:"controller"`
		} `xml:"devices"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, disk := range doc.Devices.Disks {
		if names[disk.Target.Dev] {
			t.Fatalf("duplicate disk target %s", disk.Target.Dev)
		}
		names[disk.Target.Dev] = true
	}
	if !names["sda"] || !names["sdb"] {
		t.Fatalf("missing system/seed target: %v", names)
	}
	controllers := map[string]bool{}
	for _, controller := range doc.Devices.Controllers {
		controllers[controller.Type] = true
	}
	if !controllers["sata"] || !controllers["virtio-serial"] {
		t.Fatalf("missing Windows controllers: %v", controllers)
	}
}
