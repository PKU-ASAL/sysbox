package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Discovery is a read-only lookup of an already admitted identity. No match is
// not an absence disposition: missing IDs remain unknown without residue proof.
func discoverIntentIdentity(ctx context.Context, a ActionRecord, st *state.State, g *graph.Graph, topology string) *state.Resource {
	if a.Candidate != nil || a.Action != controlplane.PlanActionCreate || a.Resource.Type != "sysbox_node" || a.DesiredDigest == "" {
		return nil
	}
	n := g.Get(a.Resource)
	if n == nil {
		return nil
	}
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok || len(cfg.Links) > 0 || len(cfg.Routes) > 0 || len(cfg.Provisioners) > 0 {
		return nil
	}
	digest, err := desiredHash(n)
	if err != nil || digest != a.DesiredDigest {
		return nil
	}
	e := NewExecutor(g, st)
	e.SetRunContext(topology, a.RunID)
	observed, err := (NodeResourceHandler{}).Discover(substrate.WithOperationIdentity(ctx, actionIdentity(a)), &ProviderContext{exec: e}, n)
	if err != nil || (observed.Status != state.ResourcePresent && observed.Observation == nil) || observed.Resource.ExternalID == "" {
		return nil
	}
	r := observed.Resource
	return &r
}

// IntentWithDiscoveredIdentity resolves only an evidence-bound detached copy.
// The original create journal and its missing candidate are never overwritten.
func IntentWithDiscoveredIdentity(a ActionRecord, observations []controlplane.RecoveredResource) (ActionRecord, error) {
	if a.Candidate != nil {
		return a, nil
	}
	if a.Action != controlplane.PlanActionCreate || a.Resource.Type != "sysbox_node" || a.DesiredDigest == "" {
		return a, ErrObservationUnknown
	}
	var found *controlplane.RecoveredResource
	for i := range observations {
		if observations[i].ActionID == a.ID {
			if found != nil {
				return a, ErrObservationUnknown
			}
			found = &observations[i]
		}
	}
	if found == nil || found.Discovered == nil || found.Status != "present" || found.ProviderScope != "local-only" {
		return a, ErrObservationUnknown
	}
	r := found.Discovered
	if r.ExternalID == "" || r.Driver == "" || !r.Address.Equal(a.Resource) || r.OwnerUID != a.OwnerUID || r.ResourceUID != a.ResourceUID || r.Generation != a.Generation || r.Str(desiredHashKey) != a.DesiredDigest || len(r.Attachments) > 0 || r.RecoveryRequired() || !resetObservedIdentity(*found, a.ID, *r, a.Status) {
		return a, ErrObservationUnknown
	}
	out := a
	candidate := cloneResetStateResource(*r)
	out.Candidate = &candidate
	out.ReplayForbidden = true
	out.EffectComplete = false
	return out, nil
}
