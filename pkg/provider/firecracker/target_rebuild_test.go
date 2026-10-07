package firecracker

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestOldManifestRequiresExplicitRebuild(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	m["version"] = 1
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.Provider.(*HandleState).ManifestPath, raw, 0600))
	_, err = s.loadManifest(context.Background(), h)
	require.ErrorContains(t, err, "rebuild")
}

func TestFirecrackerRejectsGuessedDockerBridge(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	_, err := s.Attach(context.Background(), h, driver.AttachmentRequest{Name: "nic", NetworkState: json.RawMessage(`{"nat":true,"docker_network_id":"0123456789abcdef"}`)})
	require.True(t, driver.IsCategory(err, driver.ErrorUnsupported), "%v", err)
}

func TestValidateRejectsDockerManagedNetwork(t *testing.T) {
	err := (&Substrate{}).Validate(substrate.NodeSpec{ManagedNetwork: true, ProviderConfig: &Config{AllowDirect: true, Kernel: "kernel"}})
	require.ErrorContains(t, err, "Docker NAT")
}
