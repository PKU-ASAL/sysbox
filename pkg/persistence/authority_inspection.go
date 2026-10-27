package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"sort"
)

// AuthorityInspection is one coherent ledger view, including revoked/expired
// executions still awaiting host stops. It does not expose signing credentials.
type AuthorityInspection struct {
	TopologyUID string                        `json:"topology_uid"`
	Epoch       uint64                        `json:"epoch"`
	Locked      bool                          `json:"locked"`
	Kind        string                        `json:"kind"`
	Grants      []controlplane.ExecutionGrant `json:"grants"`
	Maintenance []MaintenanceBarrier          `json:"maintenance"`
}

func (r *ExecutionGrantRepository) InspectTopology(ctx context.Context, uid string) (AuthorityInspection, error) {
	out := AuthorityInspection{TopologyUID: uid, Kind: "execution-authority", Grants: []controlplane.ExecutionGrant{}, Maintenance: []MaintenanceBarrier{}}
	if uid == "" {
		return out, ErrInvalidRecord
	}
	err := r.read(ctx, func(a *executionAuthority) error {
		out.Epoch = a.Epoch
		for _, g := range a.Grants {
			if g.Scope.TopologyUID == uid && g.State != controlplane.GrantStopped {
				out.Grants = append(out.Grants, g)
				out.Locked = true
			}
		}
		for _, b := range a.Maintenance {
			if b.TopologyUID == uid {
				out.Maintenance = append(out.Maintenance, b)
				if !b.Released {
					out.Locked = true
				}
			}
		}
		return nil
	})
	sort.Slice(out.Grants, func(i, j int) bool { return out.Grants[i].Epoch < out.Grants[j].Epoch })
	sort.Slice(out.Maintenance, func(i, j int) bool { return out.Maintenance[i].Epoch < out.Maintenance[j].Epoch })
	return out, err
}
