package hostsupervisor

import "testing"

func TestManagedSSHPathUsesCanonicalNodeUser(t *testing.T) {
	path, err := ManagedSSHPath("ubuntu", "access-1")
	if err != nil {
		t.Fatalf("ManagedSSHPath() error = %v", err)
	}
	if path != "/home/ubuntu/.ssh/authorized_keys.d/sysbox-access-1" {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestManagedSSHPathRejectsUnsafeIdentity(t *testing.T) {
	for _, tc := range [][2]string{{"", "access"}, {"ubuntu/..", "access"}, {"ubuntu", "../access"}} {
		if _, err := ManagedSSHPath(tc[0], tc[1]); err == nil {
			t.Fatalf("ManagedSSHPath(%q, %q) unexpectedly succeeded", tc[0], tc[1])
		}
	}
}
