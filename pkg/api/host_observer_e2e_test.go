//go:build e2e

package api

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostexec"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/stretchr/testify/require"
)

func wireTestHostObserver(t *testing.T, host *hostsupervisor.Server, cfg config.ServiceConfig, id string, caps []string, key ed25519.PrivateKey) string {
	t.Helper()
	bridge, err := hostexec.Open(cfg)
	require.NoError(t, err)
	require.NoError(t, bridge.BindHostIdentity(id, key.Public().(ed25519.PublicKey)))
	t.Cleanup(func() { require.NoError(t, bridge.Close()) })
	host.Observer = &hostsupervisor.Observer{AgentID: id, BootID: "test-host", Key: key, Collect: func(ctx context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error) {
		return hostexec.CollectHostObservation(ctx, hostexec.HostObservationOptions{ID: id, Capabilities: caps, CapacityPath: cfg.Paths.RunsDir}, bridge)
	}}
	publicPath := filepath.Join(t.TempDir(), "host.pub")
	require.NoError(t, os.WriteFile(publicPath, key.Public().(ed25519.PublicKey), 0600))
	return publicPath
}
