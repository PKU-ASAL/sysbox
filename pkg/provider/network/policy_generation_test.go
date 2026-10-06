package network

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestPolicyGenerationTransitions(t *testing.T) {
	id := substrate.OperationIdentity{ActionID: "apply", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "policy", Generation: 2}
	for _, tc := range []struct {
		name       string
		generation int64
		digest     string
		allowed    bool
		retry      bool
	}{
		{"retry", 2, "old", true, true}, {"same-generation-mutation", 2, "new", false, false}, {"stale", 1, "new", false, false}, {"next", 3, "new", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := id
			request.Generation = tc.generation
			retry, err := validatePolicyTransition(policyIdentityMarker(id), "old", request, tc.digest)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, tc.retry, retry)
			} else {
				require.Error(t, err)
			}
		})
	}
	foreign := id
	foreign.ResourceUID = "foreign"
	_, err := validatePolicyTransition(policyIdentityMarker(id), "old", foreign, "new")
	require.Error(t, err)
	_, err = validatePolicyTransition("", "old", id, "new")
	require.Error(t, err)
}

func TestPolicyNamespaceLockCancellationAndRelease(t *testing.T) {
	ns, err := os.Open("/proc/self/ns/net")
	require.NoError(t, err)
	defer ns.Close()
	root := t.TempDir()
	unlock, err := lockPolicyNamespaceIn(context.Background(), ns, root)
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second, err := lockPolicyNamespaceIn(ctx, ns, root)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, second)
	unlock()
	third, err := lockPolicyNamespaceIn(context.Background(), ns, root)
	require.NoError(t, err)
	third()
}
