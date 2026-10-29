package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestProvenProviderActionRequiresPersistedProof(t *testing.T) {
	record := ActionRecord{ID: "action-1", ProviderOperationID: "action-1", Quiescence: "proven"}
	require.ErrorIs(t, requireRecoveryQuiescence(record), ErrActionUncertain)
}

func TestProvenProviderActionValidatesPersistedProof(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "action-1", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 1}
	proof := substrate.QuiescenceProof{OperationID: "action-1", Anchor: "anchor", Identity: id, Evidence: map[string]any{"stopped": true}, Digest: "sha256:invalid"}
	record := ActionRecord{ID: "action-1", ProviderOperationID: "action-1", Quiescence: "proven", OwnerUID: id.OwnerUID, ResourceUID: id.ResourceUID, Topology: id.TopologyUID, Generation: id.Generation, QuiescenceProof: &proof}
	require.ErrorIs(t, requireRecoveryQuiescence(record), ErrActionUncertain)
}

func TestInterruptedProviderActionRequiresQuiescenceProof(t *testing.T) {
	record := ActionRecord{
		ID: "action-1", Topology: "topology", RunID: "run-1",
		Resource: address.Address{Type: "sysbox_node", Name: "node"},
		Action:   model.PlanActionDelete, Status: ActionUncertain,
		ProviderOperationID: "action-1", Quiescence: "unknown",
	}
	require.ErrorIs(t, requireRecoveryQuiescence(record), ErrActionUncertain)
}

func TestReturnedProviderActionDoesNotRequireQuiescenceProof(t *testing.T) {
	record := ActionRecord{ProviderOperationID: "action-1", Quiescence: "returned"}
	require.NoError(t, requireRecoveryQuiescence(record))
}

func TestLegacyIntentOnlyActionRemainsUnknown(t *testing.T) {
	record := ActionRecord{ID: "legacy", Status: ActionUncertain}
	require.ErrorIs(t, requireRecoveryQuiescence(record), ErrActionUncertain)
}
