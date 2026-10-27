package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/persistence"
	libvirtprovider "github.com/oslab/sysbox/pkg/provider/libvirt"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// Setup seeds an existing source; every transfer mutation uses a public route.
func TestOwnershipTransferPublicPreparationAndSubmission(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	defer func() { driver.DefaultRegistry = previous }()
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "libvirt", Version: "test", OwnershipAdoption: libvirtprovider.New(t.TempDir())}))
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		ctx := context.Background()
		s := stateDeleteTestServer(t, store)
		s.nodeOps = newNodeOperationStore(store)
		s.nodeService, s.scheduler, s.agentSvc = nil, nil, nil
		_, issuer, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		host, hostKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
		require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: "host", Protocol: controlplane.AgentProtocolVersion, Status: controlplane.AgentStatusOnline, Capabilities: []string{"libvirt"}, LastHeartbeat: time.Now().UTC()}))
		for _, name := range []string{"source", "target"} {
			require.NoError(t, s.workspaceService().bindPlacement(name, "host", controlplane.AgentProtocolVersion))
		}
		sourceRepo, err := s.actionRepository("source")
		require.NoError(t, err)
		targetRepo, err := s.actionRepository("target")
		require.NoError(t, err)
		source, err := sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		target, err := targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		require.NoError(t, state.NewManagerWithBackend(targetRepo.StateBackend("target")).Save(target))
		target, err = targetRepo.LoadState(ctx, "target")
		require.NoError(t, err)
		old := substrate.OperationIdentity{ActionID: "create", OwnerUID: source.TopologyUID, TopologyUID: source.TopologyUID, ResourceUID: "old", Generation: 1}
		next := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: target.TopologyUID, TopologyUID: target.TopologyUID, ResourceUID: "new", Generation: 2}
		source.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "old"), ResourceType: "sysbox_node", Driver: "libvirt", ExternalID: "external", OwnerUID: old.OwnerUID, ResourceUID: old.ResourceUID, Generation: 1}}
		require.NoError(t, state.NewManagerWithBackend(sourceRepo.StateBackend("source")).Save(source))
		source, err = sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: old, Target: next, SourceSerial: source.Meta.Serial, TargetSerial: target.Meta.Serial, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
		post := func(suffix string, body any, status int) []byte {
			t.Helper()
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			req := httptest.NewRequest("POST", "/v1/topologies/target/ownership-transfers"+suffix, bytes.NewReader(raw))
			req.Header.Set("X-Sysbox-Roles", "admin")
			response := httptest.NewRecorder()
			s.ServeHTTP(response, req)
			require.Equal(t, status, response.Code, response.Body.String())
			return response.Body.Bytes()
		}
		post("", plan, 409)
		post("/prepare", plan, 202)
		still, err := sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		require.Len(t, still.Resources, 1)
		barrier, err := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: store}).InspectMaintenance(ctx, plan.ID)
		require.NoError(t, err)
		proof, err := controlplane.SignOwnershipPreparation(hostKey, controlplane.OwnershipPreparation{Plan: plan, SourceEpoch: barrier.Epoch, HostBootID: "boot", ObservedAt: time.Now().UTC()})
		require.NoError(t, err)
		forged := proof
		forged.Signature = append([]byte(nil), proof.Signature...)
		forged.Signature[0] ^= 1
		post("/prepared", forged, 409)
		post("/prepared", proof, 200)
		var op controlplane.NodeOperation
		require.NoError(t, json.Unmarshal(post("", plan, 202), &op))
		require.Equal(t, "adoption_apply", op.Operation)
		stored, err := store.GetNodeOperation(ctx, op.ID)
		require.NoError(t, err)
		require.NotEmpty(t, stored.CommandID)
		require.NotNil(t, op.Adoption.Transfer)
		// Reconstruct API services against the same canonical backend after
		// the accepted response could have been lost.
		s = stateDeleteTestServer(t, store)
		s.nodeOps = newNodeOperationStore(store)
		s.nodeService, s.scheduler, s.agentSvc = nil, nil, nil
		require.NoError(t, s.ConfigureExecutionGrantSigner(issuer))
		s.grantHostKeys = map[string]ed25519.PublicKey{"host": host}
		var retry controlplane.NodeOperation
		require.NoError(t, json.Unmarshal(post("", plan, 202), &retry))
		require.Equal(t, op.ID, retry.ID)
		retried, err := store.GetNodeOperation(ctx, retry.ID)
		require.NoError(t, err)
		require.Equal(t, stored.CommandID, retried.CommandID)
		retired, err := sourceRepo.LoadState(ctx, "source")
		require.NoError(t, err)
		require.Empty(t, retired.Resources)
		changed := plan
		changed.ExternalID = "replacement"
		post("", changed, 409)
	})
}
