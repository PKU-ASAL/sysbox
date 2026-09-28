package runtime

import (
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type planBinding struct {
	desiredDigest, externalID, ownerUID, resourceUID string
	generation                                       int64
	present                                          bool
}

func (p *Plan) clone() *Plan {
	copy := *p
	copy.Actions = append([]controlplane.PlannedChange(nil), p.Actions...)
	return &copy
}
func (p *Plan) bind(g *graph.Graph, st *state.State) error {
	p.bound = true
	p.stateSerial = st.Meta.Serial
	p.topologyUID = st.TopologyUID
	p.bindings = map[string]planBinding{}
	for _, change := range p.Actions {
		b := planBinding{}
		if n := g.Get(change.Address); n != nil {
			hash, err := desiredHash(n)
			if err != nil {
				return err
			}
			b.desiredDigest = hash
		}
		if r := st.FindResource(change.Address); r != nil {
			b.present = true
			b.externalID = r.ExternalID
			b.ownerUID = r.OwnerUID
			b.resourceUID = r.ResourceUID
			b.generation = r.Generation
		}
		p.bindings[change.Address.String()] = b
	}
	return nil
}
func (p *Plan) validateBindings(g *graph.Graph, st *state.State) error {
	if !p.bound {
		return nil
	}
	if p.stateSerial != st.Meta.Serial || p.topologyUID != st.TopologyUID {
		return fmt.Errorf("%w: state serial or topology ownership changed", ErrPlanStale)
	}
	for _, change := range p.Actions {
		if p.excluded[change.Address.String()] {
			continue
		}
		b, ok := p.bindings[change.Address.String()]
		if !ok {
			return fmt.Errorf("%w: new target %s", ErrPlanStale, change.Address)
		}
		if b.desiredDigest != "" {
			n := g.Get(change.Address)
			if n == nil {
				return fmt.Errorf("%w: target removed", ErrPlanStale)
			}
			hash, err := desiredHash(n)
			if err != nil {
				return err
			}
			if hash != b.desiredDigest {
				return fmt.Errorf("%w: desired configuration changed for %s", ErrPlanStale, change.Address)
			}
		}
		r := st.FindResource(change.Address)
		if (r != nil) != b.present {
			return fmt.Errorf("%w: resource presence changed", ErrPlanStale)
		}
		if r != nil && (r.ExternalID != b.externalID || r.OwnerUID != b.ownerUID || r.ResourceUID != b.resourceUID || r.Generation != b.generation) {
			return fmt.Errorf("%w: resource generation or identity changed", ErrPlanStale)
		}
	}
	return nil
}
