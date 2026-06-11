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

func TestDecideNodeRecoveryCheckpointRunningAdopts(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:              RecoveryContextCheckpoint,
		HasCheckpoint:        true,
		RecoverableArtifacts: true,
		Observation: substrate.NodeObservation{
			Exists:  true,
			Running: true,
			Healthy: true,
			Status:  substrate.NodeStatusRunning,
			Reason:  "socket and process found",
		},
	})

	require.Equal(t, controlplane.RecoveryDecisionAdopt, plan.Decision)
	require.Equal(t, "socket and process found", plan.Reason)
}

func TestDecideNodeRecoveryCheckpointStoppedRecoversState(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:              RecoveryContextCheckpoint,
		HasCheckpoint:        true,
		RecoverableArtifacts: true,
		Observation: substrate.NodeObservation{
			Exists:  true,
			Running: false,
			Healthy: false,
			Status:  substrate.NodeStatusExited,
			Reason:  "vm artifacts exist but process is not running",
		},
	})

	require.Equal(t, controlplane.RecoveryDecisionRecoverState, plan.Decision)
	require.Equal(t, "vm artifacts exist but process is not running", plan.Reason)
}

func TestDecideNodeRecoveryCheckpointWithoutArtifactsIsNotFound(t *testing.T) {
	plan := DecideNodeRecovery(RecoveryInput{
		Context:              RecoveryContextCheckpoint,
		HasCheckpoint:        true,
		RecoverableArtifacts: false,
		Observation: substrate.NodeObservation{
			Exists:  false,
			Running: false,
			Healthy: false,
			Status:  substrate.NodeStatusMissing,
		},
	})

	require.Equal(t, controlplane.RecoveryDecisionNotFound, plan.Decision)
	require.Equal(t, "recoverable artifacts missing", plan.Reason)
}
