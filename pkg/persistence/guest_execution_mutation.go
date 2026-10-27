package persistence

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// GuestExecutionMutation commits a lifecycle record, optional outbox and
// authority update. File publication writes the record first; durable cancel
// intent or prior source retirement excludes new authority during replay.
type GuestExecutionMutation struct {
	Execution         controlplane.GuestExecution
	Record, Authority []byte
	Dispatch          *CommandRecord
}

type GuestExecutionTransform func(string, []byte, []byte, func(string) (*CommandRecord, error)) (*GuestExecutionMutation, error)

// Callers first prove source retirement and absence of authorization history.
// Admission is a causal lower bound even after switching to a slower API clock.
func setUnstartedGuestResult(x *controlplane.GuestExecution, status, message string, observed time.Time) {
	x.Status = status
	x.ResultClass = controlplane.GuestExecutionResultClassNotStarted
	x.Err = message
	x.Result = controlplane.GuestExecutionResult{}
	x.EndedAt = observed
	if x.EndedAt.Before(x.CreatedAt) {
		x.EndedAt = x.CreatedAt
	}
	x.ExpiresAt = x.EndedAt.Add(15 * time.Minute)
}

func originalGuestExecutionAdmission(raw []byte, x controlplane.GuestExecution, source *CommandRecord) (bool, error) {
	seed, err := guestDispatchSeed(raw)
	if err != nil {
		return false, err
	}
	if seed == nil {
		return false, nil
	}
	if source == nil || x.CreatedAt.IsZero() || !topologyName.MatchString(x.Topology) || !topologyName.MatchString(x.Node) {
		return false, ErrConflict
	}
	c, p := seed.Command, source.Command
	e := c.Execution
	if c.ID != x.CommandID || c.AgentID != x.AgentID || c.Type != "guest_execution" || c.ActionID != x.ID || c.ActionID != p.ActionID || c.TopologyUID != p.TopologyUID || c.ResourceUID != p.ResourceUID || c.Generation != p.Generation || e == nil || e.ID != x.ID || e.Topology != x.Topology || e.Node != x.Node || !e.CreatedAt.Equal(x.CreatedAt) {
		return false, ErrConflict
	}
	return true, nil
}

// The caller owns the shared command and authority locks through publication.
func (r *GuestRepository) UpdateFilesystemGuestExecutionUnderAuthority(ctx context.Context, id string, authority []byte, lookup func(string) (*CommandRecord, error), prepare GuestExecutionTransform) (*GuestExecutionMutation, error) {
	b, ok := r.records.(*fileGuestRecords)
	if !ok || !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	var prepared *GuestExecutionMutation
	err := b.updateRecord(ctx, guestExecutionKind, id, func(raw []byte) ([]byte, error) {
		var err error
		prepared, err = prepare(id, raw, authority, lookup)
		if err != nil {
			return nil, err
		}
		return prepared.Record, nil
	})
	if err != nil {
		return nil, err
	}
	return prepared, nil
}
