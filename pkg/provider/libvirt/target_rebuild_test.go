package libvirt

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAttachmentRequiresTargetIdentity(t *testing.T) {
	id := managedOperation()
	hs := &HandleState{Identity: &id, DomainUUID: "vm"}
	_, err := New().Attach(context.Background(), managedNodeHandle(hs), driver.AttachmentRequest{Name: "nic", NetworkState: json.RawMessage(`{"netns":"old","bridge":"br0","libvirt_bridge":"lv0"}`)})
	require.ErrorContains(t, err, "rebuild")
	require.Empty(t, hs.Bridges, "invalid target must not change prepared VM")
}

func TestStartRejectsUnprovenDomainInterfaces(t *testing.T) {
	bridges := []BridgeAttach{{Bridge: "owned", MAC: "02:00:00:00:00:01"}}
	valid := []byte(`<domain><devices><interface type="bridge"><source bridge="owned"/><mac address="02:00:00:00:00:01"/></interface></devices></domain>`)
	require.NoError(t, validateDomainInterfaces(valid, bridges))
	for _, raw := range []string{
		`<domain><devices><interface type="bridge"><source bridge="foreign"/><mac address="02:00:00:00:00:01"/></interface></devices></domain>`,
		`<domain><devices><interface type="bridge"><source bridge="owned"/><mac address="02:00:00:00:00:01"/></interface><interface type="bridge"><source bridge="foreign"/></interface></devices></domain>`,
		`<domain><devices/></domain>`,
	} {
		require.Error(t, validateDomainInterfaces([]byte(raw), bridges))
	}
}

func TestValidateAllowsProvenManagedNetworkAttachments(t *testing.T) {
	require.NoError(t, New().Validate(substrate.NodeSpec{ManagedNetwork: true}))
}
