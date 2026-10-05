package substrate

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestValidateNodeObservationIdentity(t *testing.T) {
	expected := OperationIdentity{OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 3, ActionID: "action"}
	good := NodeObservation{Exists: true, Status: NodeStatusRunning, OwnerUID: "owner", ResourceUID: "resource", Generation: 3}
	require.NoError(t, ValidateNodeObservationIdentity(expected, good))
	for _, bad := range []NodeObservation{{Exists: true, Status: NodeStatusRunning, OwnerUID: "other", ResourceUID: "resource", Generation: 3}, {Exists: true, Status: NodeStatusRunning, OwnerUID: "owner", ResourceUID: "resource", Generation: 2}, {Exists: true, Status: NodeStatusRunning, OwnerUID: "owner", ResourceUID: "", Generation: 3}} {
		require.Error(t, ValidateNodeObservationIdentity(expected, bad))
	}
}

var _ = fmt.Sprintf
