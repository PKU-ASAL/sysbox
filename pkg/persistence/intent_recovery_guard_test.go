package persistence

import (
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestRecoveryRequiredResourceAllowsOnlyExplicitResetOrDestroy(t *testing.T) {
	a := newAggregate()
	a.State.TopologyUID = "owner"
	r := state.Resource{Address: address.Resource("sysbox_node", "node"), OwnerUID: "owner", ResourceUID: "resource", Generation: 2}
	require.NoError(t, r.RequireRecovery("decision", r.ResourceUID, r.Generation))
	a.State.Resources = []state.Resource{r}
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	for _, op := range []string{"apply", "reset", "destroy"} {
		c := controlplane.AgentCommand{Type: "run_assigned", TopologyUID: "owner", Run: &controlplane.Run{Op: op}}
		err := ValidateCommandLifecycle(raw, "lab", c)
		if op == "apply" {
			require.ErrorIs(t, err, ErrConflict)
		} else {
			require.NoError(t, err)
		}
	}
	for _, kind := range []string{"guest_execution", "guest_file_put", "node_operation"} {
		c := controlplane.AgentCommand{Type: kind, TopologyUID: "owner", ResourceUID: "resource", Generation: 2}
		require.ErrorIs(t, ValidateCommandLifecycle(raw, "lab", c), ErrConflict)
		c.ResourceUID = "another"
		require.NoError(t, ValidateCommandLifecycle(raw, "lab", c))
	}
}
