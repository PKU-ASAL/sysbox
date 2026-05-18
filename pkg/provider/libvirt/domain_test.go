package libvirt

import (
	"strings"
	"testing"
)

func TestGenerateDomainXML_Basic(t *testing.T) {
	xml, err := GenerateDomainXML(DomainSpec{
		Name:        "test-vm",
		VCPUs:       2,
		MemoryMiB:   512,
		MachineType: "q35",
		DiskPath:    "/tmp/disk.qcow2",
		Bridges:     []BridgeAttach{{Bridge: "sysbox-br0"}},
	})
	if err != nil {
		t.Fatalf("GenerateDomainXML error: %v", err)
	}

	for _, want := range []string{
		"test-vm",
		"q35",
		"/tmp/disk.qcow2",
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
