package libvirt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionRestoresFullManagedProviderState(t *testing.T) {
	s := New(t.TempDir())
	id := managedOperation()
	dir, err := s.managedVMDir(id, "node")
	require.NoError(t, err)
	hs := &HandleState{Identity: &id, DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555", VMDir: dir, DiskPath: filepath.Join(dir, "disk.qcow2"), SSHUser: "ubuntu", SSHIP: "192.0.2.2", SSHKey: "/keys/owned", VCPUs: 2, MemoryMiB: 256}
	raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, Identity: &id, ManagedState: hs})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	t.Setenv("ADOPT_XML", path)
	virshFixture(t, "#!/bin/sh\ncase \"$1\" in\ndumpxml) cat \"$ADOPT_XML\";;\n*) exit 10;;\nesac\n")
	ctx := substrate.WithOperationIdentity(context.Background(), id)
	proof, err := s.InspectAdoption(ctx, hs.DomainUUID, substrate.NodeSpec{})
	require.NoError(t, err)
	_, err = json.Marshal(proof)
	require.NoError(t, err, "inspection evidence must cross JSON RPC")
	require.NotContains(t, string(proof.State), "/keys/owned")
	cold := New(s.vmDirRoot())
	handle, err := cold.ObserveAdoption(ctx, proof)
	require.NoError(t, err)
	require.Equal(t, hs, handle.Provider)
	for _, change := range []func(*HandleState){func(h *HandleState) { h.VMDir = "/foreign" }, func(h *HandleState) { h.DomainUUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" }, func(h *HandleState) { h.Identity = nil }} {
		bad := *hs
		change(&bad)
		xml, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, Identity: &id, ManagedState: &bad})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte(xml), 0600))
		p, err := s.InspectAdoption(ctx, hs.DomainUUID, substrate.NodeSpec{})
		if err == nil {
			_, err = cold.ApplyAdoption(ctx, p)
		}
		require.Error(t, err, "a matching XML fingerprint cannot legitimize corrupt recovery coordinates")
	}
}
