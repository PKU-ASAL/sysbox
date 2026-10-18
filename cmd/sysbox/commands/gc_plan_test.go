package commands

import (
	"bytes"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestGCPlanIsMachineReadableAndDoesNotDelete(t *testing.T) {
	root, _ := gcInventoryFixture(t)
	gcFlags(t, root, true, "")
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", t.TempDir())
	gcFakeDocker(t, nil, nil)
	gcFakeVirsh(t, "exit 0\n")
	cmd := newGCPlanCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--runs", root})
	require.NoError(t, cmd.Execute())
	var plan gcBatchPlan
	require.NoError(t, json.Unmarshal(out.Bytes(), &plan))
	require.Equal(t, 1, plan.Version)
	require.NotNil(t, plan.Candidates)
	require.True(t, plan.Complete)
}
func TestGCPlanRejectsPartialProviderInventory(t *testing.T) {
	root, _ := gcInventoryFixture(t)
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", t.TempDir())
	gcFakeDocker(t, nil, nil)
	gcFakeVirsh(t, "exit 1\n")
	cmd := newGCPlanCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--runs", root})
	require.Error(t, cmd.Execute())
	require.Empty(t, out.String())
}
func TestGCSelectionRequiresRetentionAndBoundedNodesBeforeNetworks(t *testing.T) {
	now := time.Now().UTC()
	c := controlplane.GCCandidate{Kind: "container", ExternalID: "node", OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "node", Generation: 1, InventoryDigest: "new"}
	n := c
	n.Kind = "network"
	n.ExternalID = "network"
	n.ResourceUID = "network"
	old := c
	old.InventoryDigest = "old"
	prior := gcBatchPlan{Version: 1, Complete: true, CreatedAt: now.Add(-48 * time.Hour), Candidates: []controlplane.GCCandidate{old, n}}
	selected, err := selectGCCandidates([]controlplane.GCCandidate{n, c}, &prior, 24*time.Hour, 1, now)
	require.NoError(t, err)
	require.Equal(t, []controlplane.GCCandidate{c}, selected)
	selected, err = selectGCCandidates([]controlplane.GCCandidate{c}, nil, 24*time.Hour, 32, now)
	require.NoError(t, err)
	require.Empty(t, selected)
	prior.Complete = false
	_, err = selectGCCandidates([]controlplane.GCCandidate{c}, &prior, 24*time.Hour, 32, now)
	require.Error(t, err)
}
