package firecracker

import (
	"strings"
	"testing"
)

func TestGuestHostnamePreservesShortName(t *testing.T) {
	if got := guestHostname("sysbox-lab-node-web"); got != "lab-node-web" {
		t.Fatalf("guestHostname short name = %q", got)
	}
}

func TestGuestHostnameShortensLongTopologyScopedName(t *testing.T) {
	long := "sysbox-" + strings.Repeat("long-topology-", 8) + "node-records"
	got := guestHostname(long)
	if len(got) > 63 || !strings.HasPrefix(got, "vm-") {
		t.Fatalf("guestHostname long name = %q", got)
	}
	if got != guestHostname(long) || got == guestHostname(long+"-other") {
		t.Fatalf("guestHostname is not deterministic and distinct")
	}
}
