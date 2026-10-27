package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSSHAccessHandlerRequiresBoundIdentityAndCannotOverrideExplicitGrant(t *testing.T) {
	identity := substrate.OperationIdentity{ActionID: "ssh", OwnerUID: "topology", TopologyUID: "topology", ResourceUID: "node", Generation: 1}
	request := substrate.SSHAccessMutation{ID: "ssh", ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "root", AccessID: "access"}, Delete: true}
	called := 0
	ctx := WithSSHAccessMutationHandler(context.Background(), func(_ context.Context, got substrate.SSHAccessMutation, op substrate.OperationIdentity) (substrate.SSHAccessResult, error) {
		called++
		require.Equal(t, request, got)
		require.Equal(t, identity, op)
		return substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{NodeID: "provider"}}, nil
	})
	client := NewTaskClient(nil, "provider", 1)
	_, err := client.MutateSSHAccess(ctx, request)
	require.Error(t, err)
	require.Zero(t, called)
	ctx = substrate.WithOperationIdentity(ctx, identity)
	result, err := client.MutateSSHAccess(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "provider", result.Handle.NodeID)
	require.Equal(t, 1, called)
	_, err = client.MutateSSHAccess(WithTaskGrant(ctx, "ssh", controlplane.SignedExecutionGrant{}), request)
	require.Error(t, err)
	require.Equal(t, 1, called)
}
