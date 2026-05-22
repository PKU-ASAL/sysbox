package firecracker

import "testing"

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
