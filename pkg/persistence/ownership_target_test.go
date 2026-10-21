package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipTargetReservationFencesWritesAndBindsCompletion(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "target")
	require.NoError(t, err)
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "old", OwnerUID: "source-owner", TopologyUID: "source-owner", ResourceUID: "old-resource", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "transfer", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "new-resource", Generation: 2}, TargetSerial: st.Meta.Serial, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	require.NoError(t, repo.ReserveOwnershipTarget(ctx, plan))
	require.NoError(t, repo.ReserveOwnershipTarget(ctx, plan))
	manager := state.NewManagerWithBackend(repo.StateBackend("target"))
	require.Error(t, manager.Save(st))
	_, err = manager.Snapshot(ctx, "blocked")
	require.Error(t, err)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	digest, _ := plan.Digest()
	proof, err := controlplane.SignOwnershipRetirement(key, controlplane.OwnershipRetirementProof{Plan: plan, PlanDigest: digest, SourceEpoch: 7, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()})
	require.NoError(t, err)
	resource := state.Resource{Address: address.Resource("sysbox_node", "new"), ResourceType: "sysbox_node", Driver: "docker", ExternalID: plan.ExternalID, OwnerUID: plan.Target.OwnerUID, ResourceUID: plan.Target.ResourceUID, Generation: plan.Target.Generation}
	bad := resource
	bad.ExternalID = "replacement"
	require.Error(t, repo.CommitOwnershipTarget(ctx, proof, pub, bad))
	require.NoError(t, repo.CommitOwnershipTarget(ctx, proof, pub, resource))
	require.NoError(t, repo.CommitOwnershipTarget(ctx, proof, pub, resource))
	after, err := repo.LoadState(ctx, "target")
	require.NoError(t, err)
	require.Len(t, after.Resources, 1)
	require.Equal(t, resource.ExternalID, after.Resources[0].ExternalID)
	require.Error(t, manager.Save(st))
	require.NoError(t, manager.Save(after))
	changed := plan
	changed.ExternalID = "other"
	require.Error(t, repo.ReserveOwnershipTarget(ctx, changed))
}
