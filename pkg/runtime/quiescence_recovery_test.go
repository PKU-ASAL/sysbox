package runtime

import (
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestInterruptedProviderActionRequiresQuiescenceProof(t *testing.T) {
	record := ActionRecord{
		ID: "action-1", Topology: "topology", RunID: "run-1",
		Resource: address.Address{Type: "sysbox_node", Name: "node"},
		Action:   controlplane.PlanActionDelete, Status: ActionUncertain,
		ProviderOperationID: "action-1", Quiescence: "unknown",
	}
	require.ErrorIs(t, requireRecoveryQuiescence(record), ErrActionUncertain)
}

func TestReturnedProviderActionDoesNotRequireQuiescenceProof(t *testing.T) {
	record := ActionRecord{ProviderOperationID: "action-1", Quiescence: "returned"}
	require.NoError(t, requireRecoveryQuiescence(record))
}
