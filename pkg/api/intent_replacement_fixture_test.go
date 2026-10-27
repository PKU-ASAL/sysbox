package api

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/google/uuid"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// Creates only a stopped, diskless generation in the enrolled disposable VM.
// It tests retained provider identity, not a running guest configuration failure.
func createIntentReplacementFixture(t *testing.T, root string, action runtime.ActionRecord) state.Resource {
	t.Helper()
	id := uuid.NewString()
	name := "intent-replacement-" + id
	identity := substrate.OperationIdentity{ActionID: action.ID, OwnerUID: action.OwnerUID, TopologyUID: action.OwnerUID, ResourceUID: action.ResourceUID, Generation: action.Generation}
	hs := &libvirtprovider.HandleState{Identity: &identity, DomainName: name, DomainUUID: id, ExternalDefinition: true, AdoptionFingerprint: "acceptance", NetworkInit: substrate.GuestNetworkInitPreconfigured}
	definition, err := libvirtprovider.GenerateDomainXML(libvirtprovider.DomainSpec{Name: name, UUID: id, VCPUs: 1, MemoryMiB: 128, Identity: &identity, ManagedState: hs})
	require.NoError(t, err)
	definition = regexp.MustCompile(`(?s)<disk\b.*?</disk>`).ReplaceAllString(definition, "")
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, []byte(definition), 0600))
	script := `import sys;sys.path.insert(0,'../../scripts/acceptance');import host_fencing as h;from pathlib import Path;r,m=h.two_vms.load(sys.argv[1]);h.check_scope(r,m);h.two_vms.upload(r,m,'execution',Path(sys.argv[2]),'/var/tmp/intent-replacement-'+sys.argv[3]+'.xml');print(h.guest(r,m,'execution','sudo virsh -c qemu:///system define /var/tmp/intent-replacement-'+sys.argv[3]+'.xml'))`
	out, err := exec.Command("python3", "-c", script, root, path, id).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		script := `import sys;sys.path.insert(0,'../../scripts/acceptance');import host_fencing as h;r,m=h.two_vms.load(sys.argv[1]);h.check_scope(r,m);s=h.guest(r,m,'execution','sudo virsh -c qemu:///system domstate '+sys.argv[2]);assert s.strip()=='shut off';print(h.guest(r,m,'execution','sudo virsh -c qemu:///system undefine '+sys.argv[2]))`
		out, err := exec.Command("python3", "-c", script, root, id).CombinedOutput()
		if err != nil {
			t.Errorf("exact replacement cleanup %s: %v: %s", id, err, out)
		}
	})
	replacement := *action.Candidate
	replacement.Generation = action.Generation
	replacement.ExternalID = id
	replacement.Private = nil
	raw, err := json.Marshal(hs)
	require.NoError(t, err)
	require.NoError(t, replacement.SetProviderState(raw))
	t.Log(fmt.Sprintf("isolated replacement UUID %s", id))
	return replacement
}
