package controlplane

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
)

func TestPlannedChangeUsesCanonicalAddressOnWire(t *testing.T) {
	change := PlannedChange{Address: address.StringInstance("sysbox_node", "web", "blue"), Action: PlanActionCreate}
	raw, err := json.Marshal(change)
	require.NoError(t, err)
	require.JSONEq(t, `{"address":"sysbox_node.web[\"blue\"]","action":"create"}`, string(raw))
}
