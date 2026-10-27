package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestDecideNodeRecoveryRefreshHealthy(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:  RecoveryContextRefresh,
		HasState: true,
		Observation: substrate.NodeObservation{
			Exists:  true,
			Running: true,
			Healthy: true,
			Status:  substrate.NodeStatusRunning,
			Reason:  "process alive",
		},
	})

	require.Equal(t, controlplane.RecoveryDecisionNoop, plan.Decision)
	require.Equal(t, "process alive", plan.Reason)
}

func TestDecideNodeRecoveryRefreshMissingMarksDrift(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:  RecoveryContextRefresh,
		HasState: true,
		Observation: substrate.NodeObservation{
			Exists:  false,
			Running: false,
			Healthy: false,
			Status:  substrate.NodeStatusMissing,
			Reason:  "container not found",
		},
	})

	require.Equal(t, controlplane.RecoveryDecisionMarkDrift, plan.Decision)
	require.Equal(t, "container not found", plan.Reason)
}
