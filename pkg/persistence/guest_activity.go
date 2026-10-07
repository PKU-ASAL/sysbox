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
func (r *GuestRepository) ListGuestActivity(ctx context.Context, topology string) ([]GuestActivity, error) {
	out := []GuestActivity{}
	executions, err := r.records.list(ctx, guestExecutionKind)
	if err != nil {
		return nil, err
	}
	for _, raw := range executions {
		x, err := decodeGuestRecord(raw.id, raw.data, executionCodec)
		if err != nil {
			return nil, err
		}
		if topology != "" && x.Topology != topology {
			continue
		}
		out = append(out, GuestActivity{ID: x.ID, Kind: "guest_execution", Topology: x.Topology, AgentID: x.AgentID, Status: x.Status, CreatedAt: x.CreatedAt, EndedAt: x.EndedAt})
	}
	files, err := r.records.list(ctx, guestFileKind)
	if err != nil {
		return nil, err
	}
	for _, raw := range files {
		x, err := decodeGuestRecord(raw.id, raw.data, fileOperationCodec)
		if err != nil {
			return nil, err
		}
		if topology != "" && x.Topology != topology {
			continue
		}
		out = append(out, GuestActivity{ID: x.ID, Kind: "guest_file", Topology: x.Topology, AgentID: x.AgentID, Status: x.Status, CreatedAt: x.CreatedAt, EndedAt: x.EndedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].ID < out[j].Kind+out[j].ID })
	return out, nil
}
