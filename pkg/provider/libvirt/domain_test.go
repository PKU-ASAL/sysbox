package libvirt

import (
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
