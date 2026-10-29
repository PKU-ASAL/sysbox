package runtime

import (
	"context"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Discovery is a read-only lookup of an already admitted identity. No match is
// not an absence disposition: missing IDs remain unknown without residue proof.
func discoverIntentIdentity(ctx context.Context, a ActionRecord, st *state.State, g *graph.Graph, topology string, providers *driver.Registry) *state.Resource {
	if a.Candidate != nil || a.Action != model.PlanActionCreate || (a.Resource.Type != "sysbox_node" && a.Resource.Type != "sysbox_router") || a.DesiredDigest == "" {
		return nil
	}
	n := g.Get(a.Resource)
	if n == nil {
		return nil
	}
	var substrateRef, kind string
	switch cfg := n.Data.(type) {
	case *config.NodeConfig:
		substrateRef, kind = cfg.Substrate, "node"
	case *config.RouterConfig:
		substrateRef, kind = cfg.Substrate, "router"
	default:
		return nil
	}
	digest, err := desiredHash(n)
	if err != nil || digest != a.DesiredDigest {
		return nil
	}
	e := NewExecutor(g, st, providers)
	e.SetRunContext(topology, a.RunID)
	observed, err := discoverNodeIdentity(substrate.WithOperationIdentity(ctx, actionIdentity(a)), &ProviderContext{exec: e}, n, substrateRef, kind, true)
	if err != nil || (observed.Status != state.ResourcePresent && observed.Observation == nil) || observed.Resource.ExternalID == "" {
		return nil
	}
	r := observed.Resource
	return &r
}

// IntentWithDiscoveredIdentity resolves only an evidence-bound detached copy.
// The original create journal and its missing candidate are never overwritten.
func IntentWithDiscoveredIdentity(a ActionRecord, observations []model.RecoveredResource) (ActionRecord, error) {
	if a.Candidate != nil {
		return a, nil
	}
	if a.Action != model.PlanActionCreate || (a.Resource.Type != "sysbox_node" && a.Resource.Type != "sysbox_router") || a.DesiredDigest == "" {
		return a, ErrObservationUnknown
	}
	var found *model.RecoveredResource
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
	if r.ExternalID == "" || r.Driver == "" || !r.Address.Equal(a.Resource) || r.OwnerUID != a.OwnerUID || r.ResourceUID != a.ResourceUID || r.Generation != a.Generation || r.Str(desiredHashKey) != a.DesiredDigest || found.AttachmentCount != len(r.Attachments) || r.RecoveryRequired() || !resetObservedIdentity(*found, a.ID, *r, a.Status) {
		return a, ErrObservationUnknown
	}
	names := map[string]bool{}
	for _, attachment := range r.Attachments {
		if attachment.Name == "" || names[attachment.Name] || !attachment.Node.Equal(r.Address) || attachment.Driver != r.Driver || len(attachment.DriverState) == 0 {
			return a, ErrObservationUnknown
		}
		names[attachment.Name] = true
	}
	out := a
	candidate := cloneResetStateResource(*r)
	out.Candidate = &candidate
	out.ReplayForbidden = true
	out.EffectComplete = false
	return out, nil
}
