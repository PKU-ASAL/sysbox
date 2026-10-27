package substrate

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOperationIdentityIsRequestScoped(t *testing.T) {
	id := OperationIdentity{ActionID: "action", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 2}
	ctx := WithOperationIdentity(context.Background(), id)
	got, ok := OperationIdentityFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, id, got)
	_, ok = OperationIdentityFromContext(context.Background())
	require.False(t, ok)
	require.NoError(t, id.Validate())
}

func TestOperationIdentityRejectsIncompleteOwnership(t *testing.T) {
	for _, id := range []OperationIdentity{{}, {ActionID: "action", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology"}, {ActionID: "action", OwnerUID: "owner", ResourceUID: "resource", Generation: 1}} {
		require.Error(t, id.Validate())
	}
}
