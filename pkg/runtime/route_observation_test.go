package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type routeObservationProbe struct {
	exists bool
	err    error
}

func (p routeObservationProbe) EnsureRoute(context.Context, substrate.NodeHandle, string, string) error {
	return nil
}
func (p routeObservationProbe) HasRoute(context.Context, substrate.NodeHandle, string, string) (bool, error) {
	return p.exists, p.err
}

func TestRouteObservationPreservesUnknownAndProvenAbsence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
		err    error
		want   state.ResourceStatus
	}{
		{name: "present", exists: true, want: state.ResourcePresent},
		{name: "missing", want: state.ResourceDrifted},
		{name: "timeout", err: context.DeadlineExceeded, want: state.ResourceUnknown},
		{name: "canceled", err: context.Canceled, want: state.ResourceUnknown},
		{name: "permission", err: errors.New("route probe permission denied"), want: state.ResourceUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			node := &portTestSubstrate{name: "route-test"}
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: node.name, Version: "test", Node: node, NodeState: node, GuestNetwork: routeObservationProbe{exists: tc.exists, err: tc.err}}))
			resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: node.name, ExternalID: "node-id", Attributes: state.MustAttributes(map[string]any{"routes": []any{map[string]any{"dst": "10.0.0.0/24", "via": "192.0.2.1"}}})}
			result, err := readNodeLikeResource(context.Background(), resource)
			require.Equal(t, tc.want, result.Status)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				require.Equal(t, controlplane.RecoveryDecisionUnknown, result.Decision)
				action, dictates := planActionForObservedStatus(result.Status)
				require.True(t, dictates)
				require.Equal(t, controlplane.PlanActionUnknown, action)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
