package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"sort"
	"time"
)

// GuestActivity excludes request/output contents: cleanup only needs durable
// identity and execution times, never secrets or output buffers.
type GuestActivity struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Topology  string    `json:"topology"`
	AgentID   string    `json:"agent_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	EndedAt   time.Time `json:"ended_at"`
}

func (a GuestActivity) Active() bool {
	return a.Status != controlplane.GuestExecutionCompleted && a.Status != controlplane.GuestExecutionFailed && a.Status != controlplane.GuestExecutionCancelled
}

func (a GuestActivity) Unresolved() bool {
	return a.Active() || a.CreatedAt.IsZero() || a.EndedAt.IsZero() || a.EndedAt.Before(a.CreatedAt)
}

// DecodeGuestActivity validates the complete stored record while exposing only
// the identity/status evidence needed by maintenance and deletion.
func DecodeGuestActivity(kind, id string, raw []byte) (GuestActivity, error) {
	var activity GuestActivity
	switch kind {
	case "guest_execution":
		x, err := decodeGuestRecord(id, raw, executionCodec)
		if err != nil {
			return activity, err
		}
		activity = GuestActivity{ID: x.ID, Kind: kind, Topology: x.Topology, AgentID: x.AgentID, Status: x.Status, CreatedAt: x.CreatedAt, EndedAt: x.EndedAt}
	case "guest_file":
		x, err := decodeGuestRecord(id, raw, fileOperationCodec)
		if err != nil {
			return activity, err
		}
		activity = GuestActivity{ID: x.ID, Kind: kind, Topology: x.Topology, AgentID: x.AgentID, Status: x.Status, CreatedAt: x.CreatedAt, EndedAt: x.EndedAt}
	default:
		return activity, ErrInvalidRecord
	}
	if !topologyName.MatchString(activity.Topology) {
		return GuestActivity{}, ErrCorruptRecord
	}
	return activity, nil
}
func (r *GuestRepository) ListGuestActivity(ctx context.Context, topology string) ([]GuestActivity, error) {
	out := []GuestActivity{}
	executions, err := r.records.list(ctx, guestExecutionKind)
	if err != nil {
		return nil, err
	}
	for _, raw := range executions {
		x, err := DecodeGuestActivity("guest_execution", raw.id, raw.data)
		if err != nil {
			return nil, err
		}
		if topology != "" && x.Topology != topology {
			continue
		}
		out = append(out, x)
	}
	files, err := r.records.list(ctx, guestFileKind)
	if err != nil {
		return nil, err
	}
	for _, raw := range files {
		x, err := DecodeGuestActivity("guest_file", raw.id, raw.data)
		if err != nil {
			return nil, err
		}
		if topology != "" && x.Topology != topology {
			continue
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].ID < out[j].Kind+out[j].ID })
	return out, nil
}
