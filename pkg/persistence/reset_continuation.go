package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
)

// resetContinuationMembers validates the immutable full request, independently
// of mutable progress. A receipt can remain valid after any action advances.
func resetContinuationMembers(req runtime.ResetContinuationRequest) (map[string]runtime.ActionRecord, error) {
	cp := req.Checkpoint
	if req.ID == "" || req.TopologyUID == "" || req.RunQueuedAt.IsZero() || req.ExpectedSerial < 0 || cp.StateSerialBefore < 0 || cp.StateSerialBefore > req.ExpectedSerial || cp.RunID == "" || cp.Operation != "reset" || !topologyName.MatchString(cp.Topology) || len(cp.Plan) == 0 || len(req.Existing) == 0 {
		return nil, ErrConflict
	}
	if _, err := runtime.ResumeResetPlan(&cp, &runtime.Plan{Actions: cp.Plan}); err != nil {
		return nil, ErrConflict
	}
	scope := map[string]bool{}
	for _, change := range cp.Plan {
		if change.Address.Type != "sysbox_node" {
			return nil, ErrConflict
		}
		scope[change.Address.String()] = true
	}
	members := map[string]runtime.ActionRecord{}
	for _, group := range [][]runtime.ActionRecord{req.Existing, req.Missing} {
		for _, action := range group {
			if validateAction(action) != nil || action.Action != model.PlanActionReset || action.OwnerUID != req.TopologyUID || action.Topology != cp.Topology || action.RunID != cp.RunID || !action.RunQueuedAt.Equal(req.RunQueuedAt) || action.RunRevision != req.RunRevision || !scope[action.Resource.String()] {
				return nil, ErrConflict
			}
			if _, exists := members[action.ID]; exists {
				return nil, ErrConflict
			}
			members[action.ID] = action
			delete(scope, action.Resource.String())
		}
	}
	if len(scope) != 0 {
		return nil, ErrConflict
	}
	for _, action := range req.Existing {
		if action.Revision < 1 || action.StateSerial < cp.StateSerialBefore || action.ExpectedSerial > req.ExpectedSerial {
			return nil, ErrConflict
		}
	}
	for _, action := range req.Missing {
		if action.Revision != 0 || action.Phase != "accepted" || action.Status != runtime.ActionIntentCommitted || action.EffectComplete || action.StateSerial != req.ExpectedSerial || action.ExpectedSerial != req.ExpectedSerial {
			return nil, ErrConflict
		}
	}
	return members, nil
}

// CommitResetContinuation is separate from ordinary reset admission. Callers
// must establish execution authority before invoking this capability. It only
// admits journal entries; replaying its receipt never authorizes replay of an
// unknown provider effect.
func (r *Repository) CommitResetContinuation(ctx context.Context, req runtime.ResetContinuationRequest) (*runtime.ResetContinuationReceipt, error) {
	members, err := resetContinuationMembers(req)
	if err != nil {
		return nil, err
	}
	var receipt runtime.ResetContinuationReceipt
	err = r.update(ctx, req.Checkpoint.Topology, func(a *aggregate) error {
		if previous, exists := a.ResetContinuations[req.Checkpoint.RunID]; exists {
			before, err := json.Marshal(previous.Request)
			if err != nil {
				return err
			}
			after, err := json.Marshal(req)
			if err != nil {
				return err
			}
			if !bytes.Equal(before, after) {
				return ErrConflict
			}
			receipt = previous
			return nil
		}
		if a.Serial != req.ExpectedSerial || a.State.TopologyUID != req.TopologyUID {
			return ErrConflict
		}
		existing := map[string]runtime.ActionRecord{}
		for _, action := range req.Existing {
			existing[action.ID] = action
		}
		for id, old := range a.Actions {
			if old.RunID != req.Checkpoint.RunID {
				if !runtime.ActionSettled(old) {
					return ErrConflict
				}
				continue
			}
			approved, exists := existing[id]
			if !exists || old.Revision != approved.Revision || !sameRecord(old, approved) {
				return ErrConflict
			}
			if !old.ResetContinuationSafe() {
				return runtime.ErrActionUncertain
			}
			delete(existing, id)
		}
		if len(existing) != 0 {
			return ErrConflict
		}
		for _, action := range req.Missing {
			if _, exists := a.Actions[action.ID]; exists {
				return ErrConflict
			}
			if err := commitActionIntent(a, action); err != nil {
				return err
			}
		}
		for id := range members {
			if _, exists := a.Actions[id]; !exists {
				return ErrConflict
			}
		}
		receipt = runtime.ResetContinuationReceipt{Request: req, AdmittedAt: time.Now().UTC()}
		a.ResetContinuations[req.Checkpoint.RunID] = receipt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (r *Repository) LoadResetContinuation(ctx context.Context, topology, runID string) (*runtime.ResetContinuationReceipt, error) {
	var receipt runtime.ResetContinuationReceipt
	err := r.update(ctx, topology, func(a *aggregate) error {
		var exists bool
		receipt, exists = a.ResetContinuations[runID]
		if !exists {
			return errors.Join(ErrNotFound, runtime.ErrResetContinuationNotFound)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}
