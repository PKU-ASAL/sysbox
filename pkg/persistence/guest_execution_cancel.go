package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const GuestExecutionCancelOperation = "guest_execution_cancel"

// Cancellation identity excludes status/version and private request contents.
// The original command ID and creation instant bind the execution incarnation.
func GuestExecutionCancellationCommand(x controlplane.GuestExecution) (controlplane.AgentCommand, error) {
	if !topologyName.MatchString(x.ID) || !topologyName.MatchString(x.AgentID) || !topologyName.MatchString(x.Topology) || !topologyName.MatchString(x.Node) || x.CreatedAt.IsZero() {
		return controlplane.AgentCommand{}, ErrInvalidRecord
	}
	target := controlplane.NodeOperation{ID: x.CommandID, ExternalID: x.ID, Topology: x.Topology, Node: x.Node, AgentID: x.AgentID, CreatedAt: x.CreatedAt.UTC(), Operation: GuestExecutionCancelOperation}
	raw, err := json.Marshal(target)
	if err != nil {
		return controlplane.AgentCommand{}, err
	}
	return controlplane.AgentCommand{ID: fmt.Sprintf("guest-cancel-%x", sha256.Sum256(raw)), AgentID: x.AgentID, Type: "cancel_command", Operation: target}, nil
}

// GuestExecutionCancellation is one storage transaction's prepared mutation.
// Files persist Record (including the outbox seed) before Authority; a crash
// between them is repaired by replaying the same cancellation. The durable
// intent already excludes fresh grants and renewals during that repair window.
type GuestExecutionCancellation struct {
	Execution         controlplane.GuestExecution
	Record, Authority []byte
	Dispatch          *CommandRecord
}

func PrepareGuestExecutionCancellation(id string, raw, authority []byte, lookup func(string) (*CommandRecord, error)) (*GuestExecutionCancellation, error) {
	if !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	if raw == nil {
		return nil, ErrNotFound
	}
	x, err := decodeGuestRecord(id, raw, executionCodec)
	if err != nil {
		return nil, err
	}
	if guestTerminal(x.Status) && !x.CancelRequested {
		return nil, ErrConflict
	}
	cmd, err := GuestExecutionCancellationCommand(x)
	if err != nil {
		return nil, err
	}
	cmd = DurableCommand(cmd)
	cmd.CreatedAt = time.Now().UTC()
	prior, err := lookup(cmd.ID)
	if err != nil {
		return nil, err
	}
	var dispatch *CommandRecord
	if prior != nil && prior.Retired {
		if !x.CancelRequested || prior.Command.ID != cmd.ID || prior.Command.AgentID != cmd.AgentID || prior.Command.Type != cmd.Type {
			return nil, ErrConflict
		}
	} else {
		dispatch, err = PrepareCommandEnqueue(prior, cmd)
		if err != nil {
			return nil, err
		}
	}
	wasRequested := x.CancelRequested
	if !x.CancelRequested {
		if x.Version == math.MaxInt64 {
			return nil, ErrConflict
		}
		x.CancelRequested = true
		x.EndedAt, x.ExpiresAt = time.Time{}, time.Time{}
		x.Version++
	}
	a, err := decodeExecutionAuthority(authority)
	if err != nil {
		return nil, err
	}
	if now := time.Now().UTC(); now.After(a.ObservedAt) {
		a.ObservedAt = now
	}
	if x.CommandID != "" {
		source, err := lookup(x.CommandID)
		if err != nil {
			return nil, err
		}
		if source == nil || source.Command.ID != x.CommandID || source.Command.AgentID != x.AgentID || source.Command.Type != "guest_execution" {
			return nil, ErrConflict
		}
		if !source.Retired {
			e := source.Command.Execution
			if e == nil || e.ID != x.ID || e.Topology != x.Topology || e.Node != x.Node || !e.CreatedAt.Equal(x.CreatedAt) {
				return nil, ErrConflict
			}
		}
		if source.Retired && !guestTerminal(x.Status) && !a.hasCommandExecutionHistory(x.AgentID, x.CommandID) {
			// Require the original compound admission, not merely a mutable
			// status or a missing StartedAt. Retirement excludes future delivery;
			// the already-committed cancellation excludes future grants.
			seed, err := guestDispatchSeed(raw)
			if err != nil {
				return nil, err
			}
			if seed != nil {
				e := seed.Command.Execution
				if seed.Command.ID != x.CommandID || seed.Command.AgentID != x.AgentID || seed.Command.Type != "guest_execution" || seed.Command.ActionID != source.Command.ActionID || seed.Command.TopologyUID != source.Command.TopologyUID || seed.Command.ResourceUID != source.Command.ResourceUID || seed.Command.Generation != source.Command.Generation || e == nil || e.ID != x.ID || e.Topology != x.Topology || e.Node != x.Node || !e.CreatedAt.Equal(x.CreatedAt) {
					return nil, ErrConflict
				}
				if wasRequested {
					if x.Version == math.MaxInt64 {
						return nil, ErrConflict
					}
					x.Version++
				}
				x.Status = controlplane.GuestExecutionCancelled
				x.ResultClass = controlplane.GuestExecutionResultClassNotStarted
				x.Err = ""
				x.Result = controlplane.GuestExecutionResult{}
				x.EndedAt = a.ObservedAt
				x.ExpiresAt = a.ObservedAt.Add(15 * time.Minute)

			}
		}
		for key, grant := range a.Grants {
			if grant.CommandID == x.CommandID && grant.AgentID == x.AgentID && grant.State != controlplane.GrantStopped {
				grant.State = controlplane.GrantRevoked
				a.Grants[key] = grant
			}
		}
	}
	next, err := encodeExecution(x)
	if err != nil {
		return nil, err
	}
	next, err = preserveGuestDispatch(raw, next)
	if err != nil {
		return nil, err
	}
	// A seed is immutable; mutable command rows and retirement tombstones win.
	if dispatch != nil {
		var object map[string]json.RawMessage
		if err = decodeGuestObject(next, &object); err != nil {
			return nil, err
		}
		object["cancel_dispatch_command"], err = EncodeCommandRecord(*dispatch)
		if err != nil {
			return nil, err
		}
		next, err = json.Marshal(object)
		if err != nil {
			return nil, err
		}
	}
	nextAuthority, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return &GuestExecutionCancellation{Execution: x, Record: next, Authority: nextAuthority, Dispatch: dispatch}, nil
}

func GuestExecutionCancellationSeed(raw []byte) (*CommandRecord, error) {
	var object map[string]json.RawMessage
	if err := decodeGuestObject(raw, &object); err != nil {
		return nil, err
	}
	data, ok := object["cancel_dispatch_command"]
	if !ok {
		return nil, nil
	}
	x, err := executionCodec.decode(raw)
	if err != nil {
		return nil, err
	}
	if !x.CancelRequested {
		return nil, ErrCorruptRecord
	}
	seed, err := DecodeCommandRecord(data)
	if err != nil {
		return nil, err
	}
	cmd, err := GuestExecutionCancellationCommand(x)
	if err != nil {
		return nil, err
	}
	if _, err = PrepareCommandEnqueue(seed, cmd); err != nil || seed.Retired || seed.Command.Status != controlplane.AgentCommandStatusQueued || seed.Command.Attempt != 0 || seed.Command.LeaseOwner != "" || !seed.Command.LeaseUntil.IsZero() {
		return nil, ErrCorruptRecord
	}
	return seed, nil
}

// The caller owns the shared command and authority locks through publication.
func (r *GuestRepository) CancelFilesystemGuestExecutionUnderAuthority(ctx context.Context, id string, authority []byte, lookup func(string) (*CommandRecord, error)) (*GuestExecutionCancellation, error) {
	b, ok := r.records.(*fileGuestRecords)
	if !ok || !topologyName.MatchString(id) {
		return nil, ErrInvalidRecord
	}
	var prepared *GuestExecutionCancellation
	err := b.updateRecord(ctx, guestExecutionKind, id, func(raw []byte) ([]byte, error) {
		var err error
		prepared, err = PrepareGuestExecutionCancellation(id, raw, authority, lookup)
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
