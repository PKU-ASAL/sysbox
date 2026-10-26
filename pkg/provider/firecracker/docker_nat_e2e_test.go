//go:build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	dockerprovider "github.com/oslab/sysbox/pkg/provider/docker"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// Real Docker bridge and TAP; the manifest fixture does not boot a guest.
func TestFirecrackerDockerNATAttachmentE2E(t *testing.T) {
	if os.Getenv("SYSBOX_MANAGED_BRIDGE_E2E") != "1" {
		t.Skip("requires isolated Docker host with TUN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := dockerprovider.New()
	require.NoError(t, err)
	defer docker.Close()
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "docker", Version: "test", Network: docker}))
	owner := uuid.NewString()
	netID := substrate.OperationIdentity{ActionID: "network", OwnerUID: owner, TopologyUID: owner, ResourceUID: "network", Generation: 1}
	netCtx := substrate.WithOperationIdentity(ctx, netID)
	info, err := docker.CreateManagedNetwork(netCtx, substrate.ManagedNetworkSpec{Name: "sysbox-fc-nat-" + owner, CIDR: fmt.Sprintf("10.227.%d.0/24", time.Now().UnixNano()%200+20), NAT: true, ExposeBridge: true})
	require.NoError(t, err)
	require.NotNil(t, info.BridgeTarget)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		require.NoError(t, docker.RemoveManagedNetwork(substrate.WithOperationIdentity(cleanup, netID), info.ID))
	})
	s := &Substrate{rootfsDir: t.TempDir()}
	id := "nat-" + uuid.NewString()[:8]
	hs := &HandleState{TopologyUID: owner, VMDir: filepath.Join(s.rootfsDir, owner, id, "1")}
	seedOwnedManifest(t, s, id, hs, nil)
	h := firecrackerNodeHandle(id, hs)
	raw, err := json.Marshal(map[string]any{"nat": true, "docker_network_id": info.ID, "managed_bridge": info.BridgeTarget})
	require.NoError(t, err)
	req := driver.AttachmentRequest{Name: "nat", NetworkIdentity: netID, MAC: "02:00:00:00:49:02", NetworkState: raw}
	attached, err := s.Attach(ctx, h, req)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		require.NoError(t, s.Delete(cleanup, h, req, attached.State))
	})
	again, err := s.Attach(ctx, h, req)
	require.NoError(t, err)
	require.JSONEq(t, string(attached.State), string(again.State))
	require.Equal(t, 1, hs.NICCount)
	cold := &Substrate{rootfsDir: s.rootfsDir}
	_, err = cold.Observe(ctx, h, req, attached.State)
	require.NoError(t, err)
	manifest, err := cold.loadManifest(ctx, h)
	require.NoError(t, err)
	unlock, err := lockStartAttachments(ctx, manifest)
	require.NoError(t, err)
	unlock()
	wrong := req
	wrong.NetworkIdentity.Generation++
	_, err = cold.Observe(ctx, h, wrong, attached.State)
	require.Error(t, err)
	_, err = cold.Attach(ctx, h, wrong)
	require.Error(t, err)
	found, err := cold.DiscoverManagedAttachments(ctx, h, []driver.AttachmentRequest{req})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.NoError(t, cold.Delete(ctx, h, req, attached.State))
	_, err = cold.Observe(ctx, h, req, attached.State)
	require.Error(t, err)
	t.Log("real Docker NAT bridge/TAP, exact cold proof and start preflight, idempotent attachment, stale network generation refusal and deletion passed; guest traffic not claimed")
}
