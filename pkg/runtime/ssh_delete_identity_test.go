package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestSSHDeleteRejectsForeignPathBeforeConnection(t *testing.T) {
	current := state.Resource{Address: address.Resource("sysbox_ssh_access", "access"), OwnerUID: "owner", ResourceUID: "access-uid", Generation: 1,
		Attributes: state.MustAttributes(map[string]any{"node": "sysbox_node.web", "path": "/home/other/.ssh/authorized_keys.d/sysbox-access"})}
	err := (SSHAccessResourceHandler{}).Delete(context.Background(), nil, current)
	require.Error(t, err)
	g := graph.New()
	node := address.Resource("sysbox_node", "web")
	require.NoError(t, g.AddNode(node, nil))
	require.NoError(t, g.SetData(node, &config.NodeConfig{Connections: []config.ConnectionConfig{{User: "ubuntu"}}}))
	pc := &ProviderContext{exec: NewExecutor(g, &state.State{TopologyUID: "owner"}, driver.DefaultRegistry)}
	for _, path := range []string{"/home/other/.ssh/authorized_keys.d/sysbox-access", "/home/ubuntu/.ssh/authorized_keys.d/sysbox-other"} {
		current.Attributes["path"] = path
		err := (SSHAccessResourceHandler{}).Delete(context.Background(), pc, current)
		require.ErrorContains(t, err, "identity mismatch")
	}
	current.Attributes["path"] = "/home/ubuntu/.ssh/authorized_keys.d/sysbox-access"
	current.OwnerUID = "foreign"
	require.ErrorContains(t, (SSHAccessResourceHandler{}).Delete(context.Background(), pc, current), "identity mismatch")
	current.OwnerUID = "owner"
	current.Address.Type = "sysbox_node"
	require.ErrorContains(t, (SSHAccessResourceHandler{}).Delete(context.Background(), pc, current), "identity mismatch")
}
