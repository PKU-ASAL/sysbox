package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/oslab/sysbox/pkg/driver"
)

func TestAttachNICInjectsEveryIPAndRejectsSecondNamespace(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	cfgPath := h.Provider.(*HandleState).ConfigPath
	if err := injectKernelIPArg(cfgPath, "eth0", "node", "10.1.0.2/24", ""); err != nil {
		t.Fatal(err)
	}
	if err := injectKernelIPArg(cfgPath, "eth1", "node", "10.2.0.2/24", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "10.2.0.2") {
		t.Fatal("second NIC IP was not injected")
	}
	hs := h.Provider.(*HandleState)
	hs.NICCount = 2
	hs.NetnsName = "same"
	// The namespace constraint is checked before touching network devices.
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeFixtureManifest(t, hs.VMDir, m)
	_, err = s.Attach(context.Background(), h, driver.AttachmentRequest{Name: "c", IPPrefixes: []string{"10.3.0.2/24"}, NetworkState: json.RawMessage(`{"netns":"other","bridge":"br2"}`)})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatal("expected cross-namespace attachment to be rejected")
	}
}

func TestUpsertCmdlineArg_Appends(t *testing.T) {
	got := upsertCmdlineArg("console=ttyS0 reboot=k", "ip", "ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off")
	want := "console=ttyS0 reboot=k ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off"
	if got != want {
		t.Fatalf("append mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestUpsertCmdlineArg_ReplacesSameDev(t *testing.T) {
	// Re-applying the same interface drops the previous ip= for that dev and
	// appends the new one (idempotent per interface).
	got := upsertCmdlineArg(
		"console=ttyS0 ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off reboot=k",
		"ip",
		"ip=10.0.12.21::10.0.12.254:255.255.255.0:node_db:eth0:off",
	)
	want := "console=ttyS0 reboot=k ip=10.0.12.21::10.0.12.254:255.255.255.0:node_db:eth0:off"
	if got != want {
		t.Fatalf("same-dev replace mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestUpsertCmdlineArg_AppendsDifferentDev(t *testing.T) {
	// A second NIC appends its own ip= so multi-NIC nodes get one per link.
	got := upsertCmdlineArg(
		"console=ttyS0 ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off",
		"ip",
		"ip=10.0.12.21::10.0.12.254:255.255.255.0:node_db:eth1:off",
	)
	want := "console=ttyS0 ip=10.0.12.20::10.0.12.254:255.255.255.0:node_db:eth0:off ip=10.0.12.21::10.0.12.254:255.255.255.0:node_db:eth1:off"
	if got != want {
		t.Fatalf("different-dev append mismatch\ngot:  %q\nwant: %q", got, want)
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

func TestProcessAliveRejectsInvalidPID(t *testing.T) {
	if processAlive(0) {
		t.Fatal("pid 0 should not be considered alive")
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
