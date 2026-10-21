package persistence

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestOwnershipRetirementRequiresStoppedSourceAndCannotRestoreOldOwner(t *testing.T) {
	ctx := context.Background()
	repo, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer repo.Close()
	manager := state.NewManagerWithBackend(repo.StateBackend("source"))
	st, err := manager.Load()
	require.NoError(t, err)
	source := substrate.OperationIdentity{ActionID: "original", OwnerUID: st.TopologyUID, TopologyUID: st.TopologyUID, ResourceUID: "old-resource", Generation: 1}
	resource := state.Resource{Address: address.Resource("sysbox_node", "old"), ResourceType: "sysbox_node", Driver: "libvirt", ExternalID: "uuid", OwnerUID: source.OwnerUID, ResourceUID: source.ResourceUID, Generation: 1}
	st.Resources = []state.Resource{resource}
	require.NoError(t, manager.Save(st))
	st, err = manager.Load()
	require.NoError(t, err)
	target := substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "new-owner", TopologyUID: "new-owner", ResourceUID: "new-resource", Generation: 2}
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: resource.Address.String(), TargetAddress: "sysbox_node.new", Source: source, Target: target, AgentID: "host", Provider: "libvirt", ExternalID: "uuid", SourceSerial: st.Meta.Serial, TargetSerial: 0, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	authority := NewFilesystemExecutionGrantRepository(t.TempDir())
	session, err := authority.OpenSession(ctx, "host", "process", "")
	require.NoError(t, err)
	grant, err := authority.Issue(ctx, controlplane.ExecutionGrant{AgentID: "host", SessionID: session.ID, CommandID: "old", ActionID: "old", Attempt: 1, Operation: "run_apply", Digest: "old", TargetBootID: "boot", Scope: controlplane.ExecutionScope{TopologyUID: source.TopologyUID}}, time.Minute)
	require.NoError(t, err)
	digest, err := plan.Digest()
	require.NoError(t, err)
	barrier, err := authority.BeginMaintenance(ctx, plan.ID, source.TopologyUID, digest)
	require.NoError(t, err)
	raw, err := authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	_, err = repo.RetireOwnership(ctx, plan, raw)
	require.Error(t, err)
	require.NoError(t, authority.AcceptHostStop(ctx, *grant, "verified-stop"))
	raw, err = authority.storage.ReadExecutionAuthority(ctx)
	require.NoError(t, err)
	retired, err := repo.RetireOwnership(ctx, plan, raw)
	require.NoError(t, err)
	require.Equal(t, barrier.Epoch, retired.SourceEpoch)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	proof, err := controlplane.SignOwnershipRetirement(key, retired)
	require.NoError(t, err)
	require.NoError(t, controlplane.VerifyOwnershipRetirement(pub, proof))
	current, err := manager.Load()
	require.NoError(t, err)
	require.Empty(t, current.Resources)
	current.Resources = []state.Resource{resource}
	require.Error(t, manager.Save(current), "old resource ownership must remain permanently retired")
	again, err := repo.RetireOwnership(ctx, plan, raw)
	require.NoError(t, err)
	require.Equal(t, retired, again)
	changed := plan
	changed.Target.Generation++
	_, err = repo.RetireOwnership(ctx, changed, raw)
	require.Error(t, err)
	encoded, err := json.Marshal(retired)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
}

func TestOwnershipRetirementHistorySurvivesMetadataReset(t *testing.T) {
	a := newAggregate()
	old := a.State.TopologyUID
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "old", OwnerUID: old, TopologyUID: old, ResourceUID: "old-resource", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new-resource", Generation: 2}, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	digest, err := plan.Digest()
	require.NoError(t, err)
	a.Format = OwnershipRetirementFormatVersion
	a.OwnershipRetirements = map[string]controlplane.OwnershipRetirementProof{plan.ID: {Plan: plan, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()}}
	require.NoError(t, resetAggregate(a))
	require.NotEqual(t, old, a.State.TopologyUID)
	require.NoError(t, validateAggregate(a, "source"))
	require.Len(t, a.OwnershipRetirements, 1)
	a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "reclaimed"), Driver: "docker", ExternalID: "external", OwnerUID: a.State.TopologyUID, ResourceUID: "another", Generation: 1}}
	require.Error(t, validateAggregate(a, "source"))
}
func TestGCAdmissionResetPreservesLaterOwnershipHistory(t *testing.T) {
	a := newAggregate()
	old := a.State.TopologyUID
	plan := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "docker", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "old", OwnerUID: old, TopologyUID: old, ResourceUID: "old-resource", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new-resource", Generation: 2}, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	digest, err := plan.Digest()
	require.NoError(t, err)
	a.Format = GCAdmissionFormatVersion
	a.GCReferenceFormat = FormatVersion
	a.OwnershipRetirements = map[string]controlplane.OwnershipRetirementProof{plan.ID: {Plan: plan, PlanDigest: digest, SourceEpoch: 1, SourceStateDigest: strings.Repeat("b", 64), RetiredAt: time.Now().UTC()}}
	require.NoError(t, resetAggregate(a))
	require.NotEqual(t, old, a.State.TopologyUID)
	require.NoError(t, validateAggregate(a, "source"))
	require.Len(t, a.OwnershipRetirements, 1)
	a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "reclaimed"), Driver: "docker", ExternalID: "external", OwnerUID: a.State.TopologyUID, ResourceUID: "another", Generation: 1}}
	require.Error(t, validateAggregate(a, "source"))
}
