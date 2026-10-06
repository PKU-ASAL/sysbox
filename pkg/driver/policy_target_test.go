package driver

import (
	"encoding/json"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPolicyTargetContractRoundTripAndRejectsLegacy(t *testing.T) {
	target := PolicyTarget{Resource: "sysbox_router.edge", Identity: substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "router", Generation: 2}, State: json.RawMessage(`{"container_id":"exact"}`)}
	raw, err := EncodePolicyTarget(target)
	require.NoError(t, err)
	restored, err := DecodePolicyTarget(raw)
	require.NoError(t, err)
	require.Equal(t, target, restored)
	for _, state := range []string{"null", " null ", "[]", "\"text\""} {
		invalid := target
		invalid.State = json.RawMessage(state)
		_, err := EncodePolicyTarget(invalid)
		require.Error(t, err, "provider state must be an object: %s", state)
	}
	for _, raw := range []string{`{"container_id":"exact","identity":{"generation":2}}`, `null`, `{}`, `{"resource":"router","state":{}}`} {
		_, err := DecodePolicyTarget(json.RawMessage(raw))
		require.True(t, IsCategory(err, ErrorInvalidState), "%s: %v", raw, err)
	}
}
