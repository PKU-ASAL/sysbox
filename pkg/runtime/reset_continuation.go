package runtime

import (
	"context"
	"errors"
	"time"
)

var ErrResetContinuationNotFound = errors.New("reset continuation not found")

// ResetContinuationRequest is a complete, immutable admission request. The
// execution bridge must authenticate the original checkpoint and incarnation,
// establish exclusive execution and verify old effect quiescence before use.
// Neither a checkpoint timestamp nor obtaining a lock proves quiescence.
type ResetContinuationRequest struct {
	ID             string              `json:"id"`
	TopologyUID    string              `json:"topology_uid"`
	RunQueuedAt    time.Time           `json:"run_queued_at"`
	RunRevision    string              `json:"run_revision"`
	ExpectedSerial int64               `json:"expected_serial"`
	Checkpoint     OperationCheckpoint `json:"checkpoint"`
	Existing       []ActionRecord      `json:"existing"`
	Missing        []ActionRecord      `json:"missing"`
}

// ResetContinuationReceipt retains the full request so a lost response or
// restarted controller can replay it exactly without inventing new action IDs.
type ResetContinuationReceipt struct {
	Request    ResetContinuationRequest `json:"request"`
	AdmittedAt time.Time                `json:"admitted_at"`
}

// ResetContinuationCommitter is an explicit execution capability. Observation
// wrappers implementing ActionRepository must not inherit it accidentally.
type ResetContinuationCommitter interface {
	CommitResetContinuation(context.Context, ResetContinuationRequest) (*ResetContinuationReceipt, error)
	LoadResetContinuation(context.Context, string, string) (*ResetContinuationReceipt, error)
}

// ResetContinuationSafe identifies phases whose recorded effects can enter the
// observation-driven reset engine. It is not evidence that an old writer stopped.
func (action ActionRecord) ResetContinuationSafe() bool {
	if action.Status == ActionSucceeded {
		return action.Phase == "verifying" && action.EffectComplete
	}
	if action.Status != ActionIntentCommitted && action.Status != ActionUncertain && action.Status != ActionBlocked {
		return false
	}
	switch action.Phase {
	case "accepted", "prepared", "destroy_returned", "destroyed":
		return !action.EffectComplete
	case "verifying":
		return action.EffectComplete
	default:
		return false
	}
}
