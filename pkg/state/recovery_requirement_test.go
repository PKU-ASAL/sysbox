package state

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRecoveryRequirementSurvivesPrivateStateUpdates(t *testing.T) {
	r := Resource{ResourceUID: "resource", Generation: 2}
	require.NoError(t, r.RequireRecovery("decision", "resource", 2))
	require.True(t, r.RecoveryRequired())
	require.NoError(t, r.SetProviderState([]byte(`{"vm":"same"}`)))
	require.True(t, r.RecoveryRequired())
	require.NoError(t, r.ClearRecoveryRequirement())
	require.False(t, r.RecoveryRequired())
}
