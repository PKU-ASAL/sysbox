package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/address"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type hookSubstrate struct {
	substrate.BaseSubstrate
}

func (hookSubstrate) Name() string { return "hook" }

func (hookSubstrate) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }

func (hookSubstrate) PrepareImage(context.Context, substrate.ImageSpec) (substrate.ImageRef, error) {
	return substrate.ImageRef{}, nil
}

func (hookSubstrate) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, nil
}

func (hookSubstrate) StartNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) StopNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) DestroyNode(context.Context, substrate.NodeHandle) error { return nil }

func (hookSubstrate) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}

func (hookSubstrate) AttachNIC(_ context.Context, _ substrate.NodeHandle, req substrate.LinkRequest) (substrate.AttachedNIC, error) {
	if req.KindHint == substrate.NICKindDockerNAT {
		return substrate.AttachedNIC{Kind: substrate.NICKindDockerNAT, IP: req.IP}, nil
	}
	return substrate.AttachedNIC{
		Kind:    substrate.NICKindTap,
		HostEnd: "tap0",
		IP:      req.IP,
		NetNS:   req.NetNS,
	}, nil
}

func TestWireNICsWithHookRecordsAttachPhases(t *testing.T) {
	st := &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Address:  address.Resource("sysbox_network", "nat"),
				Provider: "docker",
				Instance: map[string]any{
					"nat":               true,
					"docker_network_id": "net-1",
				},
			},
			{
				Address:  address.Resource("sysbox_network", "isolated"),
				Provider: "network",
				Instance: map[string]any{
					"netns":  "sysbox-net-isolated",
					"bridge": "br-isolated",
				},
			},
		},
	}
	var phases []string
	hook := func(phase string, _ map[string]any, fn func() error) error {
		phases = append(phases, phase)
		return fn()
	}

	result, err := wireNICsWithHook(context.Background(), hookSubstrate{}, st, substrate.NodeHandle{ID: "node"}, nil, []NICSpec{
		{Network: "nat", IP: "172.20.0.10"},
		{Network: "isolated", IP: "10.10.0.10/24"},
	}, false, "node", hook)

	require.NoError(t, err)
	require.Equal(t, []string{"attach_nat_network", "attach_nic"}, phases)
	require.Len(t, result.NICs, 2)
	require.Equal(t, "172.20.0.10", result.PrimaryIP)
}
