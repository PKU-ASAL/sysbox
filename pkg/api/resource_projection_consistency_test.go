package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

func TestInventoryMembershipIsOwnedByCurrentFirecrackerState(t *testing.T) {
	provider := "firecracker"
	st := &state.State{Meta: state.StateMeta{Serial: 3}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_network", "uplink"), Driver: "docker"},
		{Address: address.Resource("sysbox_node", "web"), Driver: provider, ExternalID: "current-instance"},
	}}
	full := []model.ResourceHealth{
		{Resource: "sysbox_network.uplink", Type: "sysbox_network", Name: "uplink", Provider: "docker", Status: model.ResourceHealthHealthy},
		{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: provider, Status: model.ResourceHealthHealthy, Observation: &substrate.NodeObservation{ExternalID: "current-instance"}},
	}
	s := NewServer(t.TempDir(), t.TempDir())
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		serial    int64
		resources []model.ResourceHealth
	}{
		{"partial", 3, full[:1]},
		{"old serial", 2, full},
		{"duplicate", 3, []model.ResourceHealth{full[0], full[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, saveConsistencyProjection(t, s, ctx, controlplane.ResourceProjection{Topology: "lab", Serial: tc.serial, ObservedAt: time.Now(), Resources: tc.resources}))
			health := s.authoritativeTopologyHealth(ctx, "lab", st)
			require.Len(t, health.Resources, 2)
			require.Equal(t, "sysbox_node.web", health.Resources[1].Resource)
			require.Equal(t, model.ResourceHealthUnknown, health.Resources[1].Status)
			require.Nil(t, health.Resources[1].Observation)
		})
	}
	wrong := append([]model.ResourceHealth{}, full...)
	wrong[1].Observation = &substrate.NodeObservation{ExternalID: "previous-instance"}
	require.False(t, resourceProjectionMatchesState(&controlplane.ResourceProjection{Serial: 3}, wrong, st))
	wrong[1].Provider = "other-provider"
	require.False(t, resourceProjectionMatchesState(&controlplane.ResourceProjection{Serial: 3}, wrong, st))
	require.NoError(t, saveConsistencyProjection(t, s, ctx, controlplane.ResourceProjection{Topology: "lab", Serial: 3, Resources: full}))
	require.Equal(t, model.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
	require.Empty(t, s.authoritativeTopologyHealth(ctx, "lab", &state.State{}).Resources)
	require.Empty(t, s.authoritativeTopologyHealth(ctx, "lab", nil).Resources)
}

func TestDockerTopologyHealthKeepsArchiveProjectionSemantics(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 3}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "docker"},
	}}
	projection := controlplane.ResourceProjection{Topology: "lab", Serial: 3, ObservedAt: time.Now(), Resources: []model.ResourceHealth{
		{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: "docker", Status: model.ResourceHealthHealthy},
	}}
	require.NoError(t, saveConsistencyProjection(t, s, ctx, projection))
	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, projection.Resources, health.Resources)
	require.Equal(t, model.ResourceHealthHealthy, health.Status)
}

func TestAgentRestartInvalidatesFirecrackerResourceHealth(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 7}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "firecracker", ExternalID: "instance-1"},
	}}
	proj := controlplane.ResourceProjection{
		AgentID: "host-a", SessionID: "old-process", Topology: "lab", Serial: 7,
		ObservedAt: time.Now(), Resources: []model.ResourceHealth{{
			Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: "firecracker",
			Status:      model.ResourceHealthHealthy,
			Observation: &substrate.NodeObservation{ExternalID: "instance-1"},
		}},
	}
	require.NoError(t, saveConsistencyProjection(t, s, ctx, proj))
	require.Equal(t, model.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	old, err := authority.ReadObservation(ctx, "host-a", "projection/lab")
	require.NoError(t, err)
	next, err := authority.OpenSession(ctx, "host-a", "replacement", old.SessionID)
	require.NoError(t, err)
	proj.SessionID = next.ID

	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, model.ResourceHealthUnknown, health.Status)
	require.Nil(t, health.Resources[0].Observation)
	proj.Sequence = 1
	require.NoError(t, s.apiStore.SaveResourceProjection(ctx, proj))
	require.Equal(t, model.ResourceHealthHealthy, s.authoritativeTopologyHealth(ctx, "lab", st).Status)
}

func TestLibvirtTopologyHealthUsesAgentProjection(t *testing.T) {
	ctx := context.Background()
	s := NewServer(t.TempDir(), t.TempDir())
	st := &state.State{Meta: state.StateMeta{Serial: 4}, Resources: []state.Resource{
		{Address: address.Resource("sysbox_network", "team"), Driver: "network", ExternalID: "sysbox-net-team"},
		{Address: address.Resource("sysbox_node", "dc"), Driver: "libvirt", ExternalID: "dc-uuid"},
	}}
	projection := controlplane.ResourceProjection{Topology: "lab", Serial: 4, ObservedAt: time.Now(), Resources: []model.ResourceHealth{
		{Resource: "sysbox_network.team", Type: "sysbox_network", Name: "team", Provider: "network", Status: model.ResourceHealthHealthy},
		{Resource: "sysbox_node.dc", Type: "sysbox_node", Name: "dc", Provider: "libvirt", Status: model.ResourceHealthHealthy, Observation: &substrate.NodeObservation{ExternalID: "dc-uuid"}},
	}}
	require.NoError(t, saveConsistencyProjection(t, s, ctx, projection))
	health := s.authoritativeTopologyHealth(ctx, "lab", st)
	require.Equal(t, model.ResourceHealthHealthy, health.Status)
	require.Len(t, health.Resources, 2)
}

func saveConsistencyProjection(t *testing.T, s *Server, ctx context.Context, p controlplane.ResourceProjection) error {
	t.Helper()
	authority := persistence.NewExecutionGrantRepository(apiExecutionGrantStorage{store: s.apiStore})
	if p.AgentID == "" {
		p.AgentID = "host-a"
	}
	old, err := authority.ReadObservation(ctx, p.AgentID, "projection/"+p.Topology)
	if err == nil && p.SessionID != "" && p.SessionID != old.SessionID {
		// Explicitly supplied session follows a session rotation in the test.
		p.Sequence = 1
	} else if err == nil {
		p.SessionID = old.SessionID
		p.Sequence = old.Sequence + 1
	} else {
		session, err := authority.OpenSession(ctx, p.AgentID, "test-process", "")
		if err != nil {
			return err
		}
		p.SessionID = session.ID
		p.Sequence = 1
	}
	if p.ObservedAt.IsZero() {
		p.ObservedAt = time.Now()
	}
	return s.apiStore.SaveResourceProjection(ctx, p)
}

func TestExpiredHostProjectionCannotRemainHealthy(t *testing.T) {
	for _, provider := range []string{"docker", "firecracker", "libvirt"} {
		t.Run(provider, func(t *testing.T) {
			s := NewServer(t.TempDir(), t.TempDir())
			st := &state.State{Resources: []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: provider}}}
			require.NoError(t, saveConsistencyProjection(t, s, context.Background(), controlplane.ResourceProjection{Topology: "lab", ObservedAt: time.Now().Add(-2 * time.Minute), Resources: []model.ResourceHealth{{Resource: "sysbox_node.web", Type: "sysbox_node", Name: "web", Provider: provider, Status: model.ResourceHealthHealthy}}}))
			require.Equal(t, model.ResourceHealthUnknown, s.authoritativeTopologyHealth(context.Background(), "lab", st).Status)
		})
	}
}
