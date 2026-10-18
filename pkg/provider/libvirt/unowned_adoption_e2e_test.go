//go:build e2e

package libvirt

import (
	"context"
	"encoding/xml"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLibvirtUnownedAdoptionColdLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_LIBVIRT_E2E") != "1" {
		t.Skip("requires disposable libvirt domain")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	name := "sysbox-adopt-" + uuid.NewString()
	external := uuid.NewString()
	raw, err := GenerateDomainXML(DomainSpec{Name: name, UUID: external, VCPUs: 1, MemoryMiB: 128})
	require.NoError(t, err)
	var domain domainXML
	require.NoError(t, xml.Unmarshal([]byte(raw), &domain))
	domain.Devices.Disks = nil
	definition, err := xml.Marshal(domain)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "domain.xml")
	require.NoError(t, os.WriteFile(path, definition, 0600))
	call := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "virsh", args...).CombinedOutput()
		require.NoError(t, err, "%v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	call("define", path)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = exec.CommandContext(cleanup, "virsh", "destroy", external).CombinedOutput()
		_, _ = exec.CommandContext(cleanup, "virsh", "undefine", external).CombinedOutput()
	}()
	root := t.TempDir()
	s := New(root)
	owner := uuid.NewString()
	identity := substrate.OperationIdentity{OwnerUID: owner, TopologyUID: owner, ResourceUID: uuid.NewString(), Generation: 1, ActionID: "adopt"}
	owned := substrate.WithOperationIdentity(ctx, identity)
	evidence, err := s.InspectAdoption(owned, external, substrate.NodeSpec{})
	require.NoError(t, err)
	require.Nil(t, evidence.ManagedIdentity)
	_, err = s.ObserveAdoption(owned, evidence)
	require.Error(t, err, "observe cannot manufacture ownership")
	h, err := s.ApplyAdoption(owned, evidence)
	require.NoError(t, err)
	cold := New(root)
	recovered, err := cold.ObserveAdoption(owned, evidence)
	require.NoError(t, err)
	require.Equal(t, h, recovered)
	other := identity
	other.OwnerUID = "foreign"
	other.TopologyUID = "foreign"
	_, err = cold.ApplyAdoption(substrate.WithOperationIdentity(ctx, other), evidence)
	require.Error(t, err)
	inspected, err := cold.InspectAdoption(owned, external, substrate.NodeSpec{})
	require.NoError(t, err)
	require.Equal(t, &identity, inspected.ManagedIdentity)
	_, err = cold.ObserveAdoption(owned, inspected)
	require.NoError(t, err)
	require.NoError(t, cold.StartNode(owned, recovered))
	require.Equal(t, "running", call("domstate", external))
	require.NoError(t, cold.Pause(owned, recovered))
	require.NoError(t, cold.Resume(owned, recovered))
	require.NoError(t, cold.DestroyNode(owned, recovered))
	present, err := gcDomainPresent(ctx, external)
	require.NoError(t, err)
	require.False(t, present)
	require.Error(t, cold.StartNode(owned, recovered), "absent adopted domain cannot be recreated")
}
