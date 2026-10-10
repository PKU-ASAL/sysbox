package runtime

import (
	"context"
	"time"
)

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
