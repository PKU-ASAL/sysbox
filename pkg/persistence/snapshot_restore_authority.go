package persistence

import (
	"context"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// CommitAuthorizedSnapshotRestore is for file-backed callers that hold the
// authority lock while this method acquires and commits the aggregate lock.
func (r *Repository) CommitAuthorizedSnapshotRestore(ctx context.Context, topology string, request SnapshotRestoreRequest, authorityRaw []byte) (SnapshotRestoreReceipt, error) {
	var receipt SnapshotRestoreReceipt
	err := r.update(ctx, topology, func(a *aggregate) error {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		next, result, err := ApplyAuthorizedSnapshotRestore(raw, authorityRaw, topology, request)
		if err != nil {
			return err
		}
		restored, err := decodeAggregate(next)
		if err != nil {
			return err
		}
		*a = *restored
		receipt = result
		return nil
	})
	if err != nil {
		return SnapshotRestoreReceipt{}, err
	}
	return receipt, nil
}

// ApplyAuthorizedSnapshotRestore is the shared transition for adapters holding
// BOTH the authority and aggregate locks until commit. It does no I/O; passing
// separately read snapshots without those locks does not establish exclusion.
// New writes require the matching frozen barrier and all grants stopped. A
// released barrier can only confirm its exact already-persisted restore receipt.
func ApplyAuthorizedSnapshotRestore(raw, authorityRaw []byte, topology string, request SnapshotRestoreRequest) ([]byte, SnapshotRestoreReceipt, error) {
	var zero SnapshotRestoreReceipt
	if !topologyName.MatchString(topology) || !validSnapshotRestoreRequest(request) {
		return nil, zero, ErrConflict
	}
	a, err := decodeAggregate(raw)
	if err != nil {
		return nil, zero, err
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, zero, err
	}
	if a.State.TopologyUID != request.TopologyUID {
		return nil, zero, ErrConflict
	}
	var authority executionAuthority
	if len(authorityRaw) == 0 || json.Unmarshal(authorityRaw, &authority) != nil {
		return nil, zero, ErrCorruptRecord
	}
	if authority.Version != executionAuthorityVersion {
		return nil, zero, ErrUnsupportedVersion
	}
	if authority.Sessions == nil || authority.Grants == nil || authority.ObservedAt.IsZero() {
		return nil, zero, ErrCorruptRecord
	}
	if err := validateMaintenance(&authority); err != nil {
		return nil, zero, err
	}
	barrier, ok := authority.Maintenance[request.ID]
	digest, err := request.Digest()
	if err != nil || !ok || barrier.TopologyUID != request.TopologyUID || barrier.RequestDigest != digest || barrier.Epoch != request.MaintenanceEpoch {
		return nil, zero, ErrConflict
	}
	if prior, ok := a.Restores[request.ID]; ok {
		if prior.Request != request {
			return nil, zero, ErrConflict
		}
		return append([]byte(nil), raw...), prior, nil
	}
	if barrier.Released {
		return nil, zero, ErrConflict
	}
	for key, grant := range authority.Grants {
		if key != grantScopeKey(grant.Scope) || !validGrant(grant) || grant.Epoch == 0 || grant.Epoch > authority.Epoch || grant.ExpiresAt.IsZero() {
			return nil, zero, ErrCorruptRecord
		}
		if grant.Scope.TopologyUID == request.TopologyUID && (grant.State != controlplane.GrantStopped || grant.StopReceipt == "") {
			return nil, zero, ErrConflict
		}
	}
	receipt, err := applySnapshotRestore(a, request)
	if err != nil {
		return nil, zero, err
	}
	if err := validateAggregate(a, topology); err != nil {
		return nil, zero, err
	}
	next, err := json.Marshal(a)
	if err != nil {
		return nil, zero, err
	}
	return next, receipt, nil
}
