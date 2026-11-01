package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
)

type authorityUnlockResult struct {
	Status  string                         `json:"status"`
	Barrier persistence.MaintenanceBarrier `json:"barrier"`
}

func (s *Server) inspectTopologyAuthority(ctx context.Context, topology string) (persistence.AuthorityInspection, error) {
	repo, err := s.actionRepository(topology)
	if err != nil {
		return persistence.AuthorityInspection{}, err
	}
	st, err := repo.LoadState(ctx, topology)
	if err != nil {
		return persistence.AuthorityInspection{}, err
	}
	return persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore}).InspectTopology(ctx, st.TopologyUID)
}

// Force unlock revokes execution authority and waits for actual host stops.
// It never removes flock files or pretends that an expired lease killed effects.
// The persistent barrier is the audit record and retry identity.
func (s *Server) forceUnlockAuthority(ctx context.Context, topology, key string) (authorityUnlockResult, error) {
	var result authorityUnlockResult
	if err := controlstore.ValidatePathSegment(topology, "topology"); err != nil {
		return result, err
	}
	if key == "" || len(key) > 128 {
		return result, fmt.Errorf("Idempotency-Key required (1-128 bytes)")
	}
	repo, err := s.actionRepository(topology)
	if err != nil {
		return result, err
	}
	st, err := repo.LoadState(ctx, topology)
	if err != nil {
		return result, err
	}
	id := fmt.Sprintf("unlock-%x", sha256.Sum256([]byte(topology+"\x00"+key)))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("sysbox.force-unlock.v1\x00"+topology+"\x00"+st.TopologyUID)))
	authority := persistence.NewExecutionGrantRepository(controlstore.AuthorityStorage{Store: s.apiStore})
	barrier, err := authority.BeginMaintenance(ctx, id, st.TopologyUID, digest)
	if err != nil {
		return result, err
	}
	result.Barrier = *barrier
	if barrier.Released {
		result.Status = "unlocked"
		return result, nil
	}
	result.Status = "waiting_for_stop"
	if err := s.workspaceService().EnsureTopologyQuiescent(ctx, topology); err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return result, nil
		}
		return result, err
	}
	if err := authority.EndMaintenance(ctx, *barrier); err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return result, nil
		}
		return result, err
	}
	result.Status = "unlocked"
	result.Barrier.Released = true
	return result, nil
}
