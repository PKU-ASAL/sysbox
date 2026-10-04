package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAgentInventoryCodecRejectsCorruptionAndNormalizesTime(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, s apiStore) {
		now := time.Date(2026, 10, 4, 12, 0, 0, 123456789, time.FixedZone("local", 8*3600))
		inv := controlplane.AgentInventory{AgentID: "agent", ObservedAt: now, Status: "healthy"}
		require.NoError(t, s.SaveAgentInventory(context.Background(), inv))
		got, err := s.GetAgentInventory(context.Background(), "agent")
		require.NoError(t, err)
		require.True(t, now.Equal(got.ObservedAt))
		require.Equal(t, "agent", got.AgentID)
	})
}
