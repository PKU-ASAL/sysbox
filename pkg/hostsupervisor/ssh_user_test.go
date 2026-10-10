package hostsupervisor

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestCanonicalGuestUserUsesPersistedDesiredConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desired any
		want    string
		invalid bool
	}{
		{"connection", map[string]any{"connections": []config.ConnectionConfig{{User: "ubuntu"}}, "provider_config": map[string]any{"SSHUser": "root"}}, "ubuntu", false},
		{"provider", map[string]any{"provider_config": map[string]any{"SSHUser": "debian"}}, "debian", false},
		{"unspecified", map[string]any{}, "", false},
		{"absent", nil, "", false},
		{"wrong-shape", "corrupt", "", true},
		{"wrong-connections", map[string]any{"connections": "corrupt"}, "", true},
		{"bad-user", map[string]any{"connections": []config.ConnectionConfig{{User: "../root"}}}, "", true},
		{"later-user-not-selected", map[string]any{"connections": []config.ConnectionConfig{{}, {User: "other"}}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := state.Resource{Attributes: state.MustAttributes(map[string]any{"desired": tc.desired})}
			got, err := canonicalGuestUser(resource)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestResolverRestoresGuestUserAfterCanonicalIdentityObservation(t *testing.T) {
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	node := &candidateNode{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "ssh-user", Version: "1", Node: node, NodeState: node}))
	r := CanonicalResolver{AgentID: "agent"}
	ref := TargetRef{Topology: "lab", Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}}
	resource := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "ssh-user", ExternalID: "provider-id", OwnerUID: "owner", ResourceUID: "resource", Generation: 1,
		Attributes: state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []config.ConnectionConfig{{User: "ubuntu"}}}})}
	target, err := r.resolveResource(context.Background(), ref, &resource)
	require.NoError(t, err)
	connection, err := canonicalConnection(target)
	require.NoError(t, err)
	require.Equal(t, "ubuntu", connection.User)
	resource.Attributes = state.MustAttributes(map[string]any{"desired": map[string]any{"connections": []config.ConnectionConfig{{User: "../invalid"}}}})
	target, err = r.resolveResource(context.Background(), ref, &resource)
	require.NoError(t, err, "provider-only operations must not depend on SSH username validity")
	_, err = canonicalConnection(target)
	require.Error(t, err)
	require.Equal(t, 1, node.connections, "invalid user must be rejected before connecting")
	node.wrongIdentity = true
	_, err = r.resolveResource(context.Background(), ref, &resource)
	require.Error(t, err)
}
