package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestAdoptionStateRetryPreservesCachedRepository(t *testing.T) {
	s := NewServer(t.TempDir(), t.TempDir())
	s.stateBackend = "sqlite://" + filepath.Join(t.TempDir(), "lifecycle.db")
	ctx := context.Background()
	repo, err := s.actionRepository("lab")
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	op := controlplane.NodeOperation{ID: "adopt", Adoption: &controlplane.AdoptionRequest{
		ID: "adopt", AgentID: "agent", Topology: "lab", TopologyUID: st.TopologyUID,
		Address: "sysbox_node.web", Provider: "docker", ExternalID: "external", ResourceUID: "node", Generation: 1, ExpectedSerial: st.Meta.Serial,
		Evidence: substrate.AdoptionEvidence{Provider: "docker", ExternalID: "external", Fingerprint: "fingerprint"},
	}}
	evidence := json.RawMessage(`{"container_name":"web"}`)
	require.NoError(t, s.commitAdoptionState(ctx, op, evidence))
	require.NoError(t, s.commitAdoptionState(ctx, op, evidence))
	after, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.Equal(t, st.Meta.Serial+1, after.Meta.Serial)
	require.Len(t, after.Resources, 1)
}
