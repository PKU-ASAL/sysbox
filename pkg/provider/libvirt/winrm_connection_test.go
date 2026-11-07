package libvirt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestExplicitWinRMBindsGuestIPAndKeepsDurableHelper(t *testing.T) {
	s := New()
	h := substrate.NodeHandle{ID: "domain", Net: substrate.NetInfo{PrimaryIP: "192.0.2.10"}, Provider: &HandleState{DomainUUID: "domain", GuestFamily: substrate.GuestFamilyWindows}}
	hint := substrate.ConnectionHint{Type: "winrm", Host: "https://192.0.2.10:5986/wsman", User: "operator", Password: "resolved"}
	conn, err := s.Connection(h, []substrate.ConnectionHint{hint})
	require.NoError(t, err)
	require.IsType(t, &winRMDomainConnection{}, conn)
	_, ok := conn.(substrate.ConnectionStarter)
	require.True(t, ok)
	_, ok = conn.(substrate.ConnectionExecutionCanceler)
	require.True(t, ok)
	conn, err = s.Connection(h, nil)
	require.NoError(t, err)
	require.IsType(t, &qgaDomainConnection{}, conn)
	hint.Host = "https://192.0.2.11:5986/wsman"
	_, err = s.Connection(h, []substrate.ConnectionHint{hint})
	require.Error(t, err)
	hint.Host = "https://192.0.2.10:5986/wsman"
	hint.InsecureSkipHostKeyCheck = true
	_, err = s.Connection(h, []substrate.ConnectionHint{hint})
	require.Error(t, err)
}
func TestWinRMRejectsImplicitNamespaceEscapeAndLinux(t *testing.T) {
	s := New()
	hint := substrate.ConnectionHint{Type: "winrm", Host: "https://192.0.2.10:5986/wsman", User: "operator", Password: "resolved"}
	h := substrate.NodeHandle{ID: "domain", Net: substrate.NetInfo{PrimaryIP: "192.0.2.10"}, Provider: &HandleState{GuestFamily: substrate.GuestFamilyWindows, Bridges: []BridgeAttach{{Netns: "isolated"}}}}
	_, err := s.Connection(h, []substrate.ConnectionHint{hint})
	require.ErrorContains(t, err, "namespace")
	h.Provider = &HandleState{GuestFamily: substrate.GuestFamilyLinux}
	_, err = s.Connection(h, []substrate.ConnectionHint{hint})
	require.Error(t, err)
}
func TestWinRMDoesNotAcceptUnrelatedSSHOverride(t *testing.T) {
	h := substrate.NodeHandle{ID: "domain", Provider: &HandleState{GuestFamily: substrate.GuestFamilyWindows, SSHIP: "192.0.2.99"}}
	_, err := New().Connection(h, []substrate.ConnectionHint{{Type: "winrm", Host: "https://192.0.2.99:5986/wsman", User: "operator", Password: "resolved"}})
	require.ErrorContains(t, err, "managed guest addresses")
}

func TestWinRMRequestBudgetIncludesDomainValidation(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "virsh"), []byte("#!/bin/sh\nexec sleep 2\n"), 0700))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	identity := &substrate.OperationIdentity{ActionID: "inspect", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	h := substrate.NodeHandle{ID: "11111111-1111-4111-8111-111111111111", Generation: 1, Net: substrate.NetInfo{PrimaryIP: "192.0.2.10"}, Provider: &HandleState{DomainUUID: "11111111-1111-4111-8111-111111111111", GuestFamily: substrate.GuestFamilyWindows, Identity: identity}}
	conn, err := New().Connection(h, []substrate.ConnectionHint{{Type: "winrm", Host: "https://192.0.2.10:5986/wsman", User: "operator", Password: "resolved"}})
	require.NoError(t, err)
	started := time.Now()
	_, err = conn.Exec(context.Background(), substrate.ExecRequest{Program: "exit 0", Shell: substrate.ShellPowerShell, Timeout: 30 * time.Millisecond}, nil, nil)
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
	started = time.Now()
	err = conn.(substrate.ConnectionWaiter).WaitReady(context.Background(), 30*time.Millisecond)
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second)
}
