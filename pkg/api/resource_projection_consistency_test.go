package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestInventoryMembershipIsOwnedByCurrentFirecrackerState(t *testing.T) {
	provider := "firecracker"
	st := &state.State{Meta: state.StateMeta{Serial: 3}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_network", "uplink"), Driver: "docker"},
		{Address: address.Resource("sysbox_node", "web"), Driver: provider, ExternalID: "current-instance"},
	}}
	full := []controlplane.ResourceHealth{
		{Resource: "sysbox_network.uplink", Type: "sysbox_network", Name: "uplink", Provider: "docker", Status: controlplane.ResourceHealthHealthy},
		{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: provider, Status: controlplane.ResourceHealthHealthy, Observation: &substrate.NodeObservation{ExternalID: "current-instance"}},
	}
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		serial    int64
		resources []controlplane.ResourceHealth
	}{
		{"partial", 3, full[:1]},
		{"old serial", 2, full},
		{"duplicate", 3, []controlplane.ResourceHealth{full[0], full[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, s.apiStore.SaveResourceProjection(ctx, controlplane.ResourceProjection{Topology: "lab", Serial: tc.serial, ObservedAt: time.Now(), Resources: tc.resources}))
			health := s.authoritativeTopologyHealth(ctx, "lab", st)
			require.Len(t, health.Resources, 2)
			require.Equal(t, "sysbox_node.web", health.Resources[1].Resource)
			require.Equal(t, controlplane.ResourceHealthUnknown, health.Resources[1].Status)
			require.Nil(t, health.Resources[1].Observation)
		})
	}
	wrong := append([]controlplane.ResourceHealth{}, full...)
	wrong[1].Observation = &substrate.NodeObservation{ExternalID: "previous-instance"}
	require.False(t, resourceProjectionMatchesState(&controlplane.ResourceProjection{Serial: 3}, wrong, st))
	wrong[1].Provider = "other-provider"
	require.False(t, resourceProjectionMatchesState(&controlplane.ResourceProjection{Serial: 3}, wrong, st))
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, controlplane.ResourceProjection{Topology: "lab", Serial: 3, Resources: full}))
	require.Equal(t, controlplane.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
	require.Empty(t, s.authoritativeTopologyHealth(ctx, "lab", &state.State{}).Resources)
	require.Empty(t, s.authoritativeTopologyHealth(ctx, "lab", nil).Resources)
}

func TestDockerTopologyHealthKeepsArchiveProjectionSemantics(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 3}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "docker"},
	}}
	projection := controlplane.ResourceProjection{Topology: "lab", Serial: 3, ObservedAt: time.Now(), Resources: []controlplane.ResourceHealth{
		{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: "docker", Status: controlplane.ResourceHealthHealthy},
	}}
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, projection))
	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, projection.Resources, health.Resources)
	require.Equal(t, controlplane.ResourceHealthHealthy, health.Status)
}

func TestAgentRestartInvalidatesFirecrackerResourceHealth(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 7}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "firecracker", ExternalID: "instance-1"},
	}}
	proj := controlplane.ResourceProjection{
		AgentID: "host-a", SessionID: "old-process", Topology: "lab", Serial: 7,
		ObservedAt: time.Now(), Resources: []controlplane.ResourceHealth{{
			Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: "firecracker",
			Status:      controlplane.ResourceHealthHealthy,
			Observation: &substrate.NodeObservation{ExternalID: "instance-1"},
		}},
	}
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, proj))
	require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: "host-a", Status: "online", Labels: map[string]string{controlplane.AgentSessionLabel: "old-process"}}))
	require.Equal(t, controlplane.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
	require.NoError(t, s.agentService().Save(ctx, controlplane.Agent{ID: "host-a", Status: "online", Labels: map[string]string{controlplane.AgentSessionLabel: "new-process"}}))
	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, controlplane.ResourceHealthUnknown, health.Status)
	require.Nil(t, health.Resources[0].Observation)
	proj.SessionID = "new-process"
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, proj))
	require.Equal(t, controlplane.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
}

func TestLibvirtTopologyHealthUsesAgentProjection(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 4}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_network", "team"), Driver: "network", ExternalID: "sysbox-net-team"},
		{Address: address.Resource("sysbox_node", "dc"), Driver: "libvirt", ExternalID: "dc-uuid"},
	}}
	projection := controlplane.ResourceProjection{Topology: "lab", Serial: 4, ObservedAt: time.Now(), Resources: []controlplane.ResourceHealth{
		{Resource: "sysbox_network.team", Type: "sysbox_network", Name: "team", Provider: "network", Status: controlplane.ResourceHealthHealthy},
		{Resource: "sysbox_node.dc", Type: "sysbox_node", Name: "dc", Provider: "libvirt", Status: controlplane.ResourceHealthHealthy, Observation: &substrate.NodeObservation{ExternalID: "dc-uuid"}},
	}}
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, projection))
	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, controlplane.ResourceHealthHealthy, health.Status)
	require.Len(t, health.Resources, 2)
}
