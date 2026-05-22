package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertCmdlineArg_Appends(t *testing.T) {
	got := upsertCmdlineArg("console=ttyS0 reboot=k", "ip", "ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off")
	want := "console=ttyS0 reboot=k ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off"
	if got != want {
		t.Fatalf("append mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestUpsertCmdlineArg_Replaces(t *testing.T) {
	got := upsertCmdlineArg("console=ttyS0 ip=oldvalue reboot=k", "ip", "ip=newvalue")
	want := "console=ttyS0 ip=newvalue reboot=k"
	if got != want {
		t.Fatalf("replace mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSplitCIDR(t *testing.T) {
	cases := []struct {
		in       string
		wantIP   string
		wantMask string
	}{
		{"10.0.12.20/24", "10.0.12.20", "255.255.255.0"},
		{"192.168.1.5/16", "192.168.1.5", "255.255.0.0"},
		{"172.22.0.10/30", "172.22.0.10", "255.255.255.252"},
	}
	for _, c := range cases {
		ip, mask, err := splitCIDR(c.in)
		if err != nil {
			t.Fatalf("splitCIDR(%q) error: %v", c.in, err)
		}
		if ip != c.wantIP || mask != c.wantMask {
			t.Fatalf("splitCIDR(%q) = (%q, %q), want (%q, %q)", c.in, ip, mask, c.wantIP, c.wantMask)
		}
	}
}

func TestSplitCIDR_Rejects(t *testing.T) {
	if _, _, err := splitCIDR("not-a-cidr"); err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

func TestFirecrackerPIDForSocketReturnsZeroWhenMissing(t *testing.T) {
	if got := firecrackerPIDForSocket("/tmp/sysbox-no-such-firecracker.sock"); got != 0 {
		t.Fatalf("firecrackerPIDForSocket missing = %d, want 0", got)
	}
}

func TestReadPIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firecracker.pid")
	if got := readPIDFile(path); got != 0 {
		t.Fatalf("read missing pid = %d, want 0", got)
	}
	if err := os.WriteFile(path, []byte("1234\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readPIDFile(path); got != 1234 {
		t.Fatalf("read pid = %d, want 1234", got)
	}
}

func TestProcessAliveRejectsInvalidPID(t *testing.T) {
	if processAlive(0) {
		t.Fatal("pid 0 should not be considered alive")
	}
}

func TestWriteVMMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysbox.json")
	if err := writeVMMetadata(path, &HandleState{
		VMDir:       filepath.Join(t.TempDir(), "vm-1"),
		Socket:      "/tmp/fc.sock",
		ConfigPath:  "/tmp/vm.json",
		PIDFile:     "/tmp/fc.pid",
		CrashPolicy: "observe_only",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || !strings.Contains(string(data), `"managed_by": "sysbox"`) {
		t.Fatalf("metadata missing managed_by: %s", data)
	}
}

func TestProcessAnchorSupportsJSONAndLegacyPID(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "firecracker.json.pid")
	want := processAnchor{PID: 1234, StartTime: "999", Socket: "/tmp/fc.sock", VMID: "vm-1"}
	if err := writeProcessAnchor(jsonPath, want); err != nil {
		t.Fatal(err)
	}
	if got := readProcessAnchor(jsonPath); got != want {
		t.Fatalf("json anchor = %+v, want %+v", got, want)
	}

	legacyPath := filepath.Join(dir, "firecracker.pid")
	if err := os.WriteFile(legacyPath, []byte("4321\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readProcessAnchor(legacyPath); got.PID != 4321 {
		t.Fatalf("legacy pid = %+v, want pid 4321", got)
	}
}

func TestProcessMatchesRejectsMismatchedStartTime(t *testing.T) {
	if processMatches(os.Getpid(), "definitely-not-current-start-time") {
		t.Fatal("processMatches should reject mismatched start time")
	}
	if !processMatches(os.Getpid(), processStartTime(os.Getpid())) {
		t.Fatal("processMatches should accept current process start time")
	}
}
