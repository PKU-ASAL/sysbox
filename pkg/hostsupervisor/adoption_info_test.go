package hostsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestAdoptionBootInfoDoesNotResolveUnadoptedNode(t *testing.T) {
	c, req, g := adoptionFixture(t)
	resolved := false
	s := Server{Adoption: c, Power: &PowerDispatcher{Resolve: func(context.Context, TargetRef) (NodeTarget, error) {
		resolved = true
		return NodeTarget{}, errors.New("not in canonical state yet")
	}}}
	target := TargetRef{Topology: req.Topology, Node: "web", Scope: g.Grant.Scope}
	out := s.Handle(context.Background(), Request{Version: 1, Operation: "adoption-info", Target: target})
	require.Empty(t, out.Error)
	require.Equal(t, c.HostBootID, out.BootID)
	require.False(t, resolved)
	s.Adoption = nil
	out = s.Handle(context.Background(), Request{Version: 1, Operation: "adoption-info", Target: target})
	require.NotEmpty(t, out.Error)
}

func TestAdoptionSocketRejectsInconsistentOuterTarget(t *testing.T) {
	c, req, g := adoptionFixture(t)
	called := false
	s := Server{Adoption: c, AdoptionApply: func(context.Context, controlplane.AdoptionRequest, controlplane.SignedExecutionGrant) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{}`), nil
	}}
	for _, target := range []TargetRef{
		{Topology: "other", Node: "web", Scope: g.Grant.Scope},
		{Topology: req.Topology, Node: "other", Scope: g.Grant.Scope},
		{Topology: req.Topology, Node: "web", Scope: controlplane.ExecutionScope{TopologyUID: g.Grant.Scope.TopologyUID, ResourceUID: g.Grant.Scope.ResourceUID, Generation: 2}},
	} {
		out := s.Handle(context.Background(), Request{Version: 1, Operation: "adoption-apply", Target: target, AdoptionRequest: &req, Grant: &g})
		require.NotEmpty(t, out.Error)
	}
	require.False(t, called)
}
