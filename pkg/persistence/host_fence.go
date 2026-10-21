package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type HostFenceRecord struct {
	GCDisposition  *GCFenceDisposition            `json:"gc_disposition,omitempty"`
	Reconciliation *FenceDeleteReconciliation     `json:"delete_reconciliation,omitempty"`
	Release        *HostFenceRelease              `json:"release,omitempty"`
	Request        controlplane.HostFenceRequest  `json:"request"`
	BarrierID      string                         `json:"barrier_id"`
	AcceptedAt     time.Time                      `json:"accepted_at"`
	Receipt        *controlplane.HostFenceReceipt `json:"receipt,omitempty"`
}

func cloneFence(record HostFenceRecord) (HostFenceRecord, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return HostFenceRecord{}, err
	}
	var result HostFenceRecord
	err = json.Unmarshal(raw, &result)
	return result, err
}
func (r *ExecutionGrantRepository) BeginHostFence(ctx context.Context, request controlplane.HostFenceRequest) (HostFenceRecord, error) {
	var result HostFenceRecord
	digest, err := request.Digest()
	if err != nil {
		return result, err
	}
	err = r.update(ctx, func(a *executionAuthority) error {
		if prior, ok := a.Fences[request.ID]; ok {
			old, err := prior.Request.Digest()
			if err != nil || old != digest {
				return ErrConflict
			}
			result, err = cloneFence(prior)
			return err
		}
		g, ok := a.Grants[grantScopeKey(request.Grant.Scope)]
		if !ok || !sameGrantEpoch(g, request.Grant) || !g.ExpiresAt.Equal(request.Grant.ExpiresAt) || g.State == controlplane.GrantStopped || a.maintenanceBlocks(g.Scope.TopologyUID) || a.Epoch == math.MaxUint64 {
			return ErrConflict
		}
		if a.hostFenceBlocks(g.AgentID) {
			return ErrConflict
		}
		for _, other := range a.Grants {
			if other.AgentID == g.AgentID && other.State != controlplane.GrantStopped && (other.Scope.TopologyUID != g.Scope.TopologyUID || (g.Operation == "gc_delete" && !sameGrantEpoch(other, g))) {
				return ErrConflict
			}
		}
		barrierID := "fence-" + request.ID
		if _, exists := a.Maintenance[barrierID]; exists {
			return ErrConflict
		}
		if a.Maintenance == nil {
			a.Maintenance = map[string]MaintenanceBarrier{}
		}
		a.Epoch++
		a.Maintenance[barrierID] = MaintenanceBarrier{ID: barrierID, TopologyUID: g.Scope.TopologyUID, RequestDigest: digest, Epoch: a.Epoch, CreatedAt: a.ObservedAt}
		for id, grant := range a.Grants {
			if grant.Scope.TopologyUID == g.Scope.TopologyUID && grant.State == controlplane.GrantActive {
				grant.State = controlplane.GrantRevoked
				a.Grants[id] = grant
			}
		}
		if a.Fences == nil {
			a.Fences = map[string]HostFenceRecord{}
		}
		copy, err := cloneFence(HostFenceRecord{Request: request, BarrierID: barrierID, AcceptedAt: a.ObservedAt})
		if err != nil {
			return err
		}
		a.Fences[request.ID] = copy
		result, err = cloneFence(copy)
		return err
	})
	return result, err
}
func (r *ExecutionGrantRepository) ReadHostFence(ctx context.Context, id string) (HostFenceRecord, error) {
	var result HostFenceRecord
	err := r.read(ctx, func(a *executionAuthority) error {
		record, ok := a.Fences[id]
		if !ok {
			return ErrNotFound
		}
		var err error
		result, err = cloneFence(record)
		return err
	})
	return result, err
}

// Receipt persistence is evidence only. It does not stop a grant, release a
// barrier, finish a Run or mutate any resource/action outcome.
func (r *ExecutionGrantRepository) RecordHostFence(ctx context.Context, id string, receipt controlplane.HostFenceReceipt) error {
	return r.update(ctx, func(a *executionAuthority) error {
		record, ok := a.Fences[id]
		if !ok {
			return ErrNotFound
		}
		if err := controlplane.VerifyHostFenceReceipt(record.Request, receipt); err != nil {
			return err
		}
		if receipt.ObservedAt.Before(record.AcceptedAt) || receipt.ObservedAt.After(a.ObservedAt.Add(controlplane.ExecutionGrantMaxClockSkew)) {
			return fmt.Errorf("fence observation time outside admitted request")
		}
		if record.Receipt != nil {
			old, _ := json.Marshal(record.Receipt)
			next, _ := json.Marshal(receipt)
			if !bytes.Equal(old, next) {
				return ErrConflict
			}
			return nil
		}
		barrier, ok := a.Maintenance[record.BarrierID]
		if !ok || barrier.Released {
			return ErrConflict
		}
		record.Receipt = &receipt
		copy, err := cloneFence(record)
		if err != nil {
			return err
		}
		a.Fences[id] = copy
		return nil
	})
}
func validateHostFences(a *executionAuthority) error {
	for id, record := range a.Fences {
		digest, err := record.Request.Digest()
		barrier, ok := a.Maintenance[record.BarrierID]
		if err != nil || id != record.Request.ID || record.AcceptedAt.IsZero() || !ok || barrier.Released != record.released() || record.BarrierID != "fence-"+record.Request.ID || barrier.TopologyUID != record.Request.Grant.Scope.TopologyUID || barrier.RequestDigest != digest || !barrier.CreatedAt.Equal(record.AcceptedAt) {
			return ErrCorruptRecord
		}
		if record.Release != nil {
			stop, ok := a.Stops[record.Request.Grant.Epoch]
			if !ok || !sameGrantEpoch(stop.Grant, record.Request.Grant) || record.Release.ReleasedAt.After(a.ObservedAt) {
				return ErrCorruptRecord
			}
		}
		if err := validateFenceDelete(record, a); err != nil {
			return err
		}
		if err := validateGCDisposition(record, a); err != nil {
			return err
		}
		if err := validateFenceRelease(record); err != nil {
			return err
		}
		if record.Receipt != nil && (controlplane.VerifyHostFenceReceipt(record.Request, *record.Receipt) != nil || record.Receipt.ObservedAt.Before(record.AcceptedAt)) {
			return ErrCorruptRecord
		}
	}
	return nil
}

// Evidence acceptance never resumes a host. Only an explicit validated release
// lifts the host hold; session changes and unrelated topologies cannot bypass it.
func (a *executionAuthority) hostFenceBlocks(agent string) bool {
	for _, record := range a.Fences {
		if record.Request.Grant.AgentID == agent && !record.released() {
			return true
		}
	}
	return false
}
