package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type ObservationRequest struct {
	SessionID string `json:"session_id"`
}

// Observer owns provider reads. Worker requests contain no topology, path,
// endpoint or credentials; scope and data come from host-owned configuration.
type Observer struct {
	AgentID  string
	BootID   string
	Key      ed25519.PrivateKey
	Collect  func(context.Context) (controlplane.AgentInventory, []controlplane.ResourceProjection, error)
	mu       sync.Mutex
	sequence uint64
}

func (o *Observer) Snapshot(ctx context.Context, request ObservationRequest) (controlplane.HostObservation, error) {
	if o == nil || o.Collect == nil || request.SessionID == "" || len(request.SessionID) > 128 {
		return controlplane.HostObservation{}, fmt.Errorf("host observation request unavailable or invalid")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return controlplane.HostObservation{}, err
	}
	now := time.Now().UTC()
	sequence := uint64(now.UnixNano())
	if sequence <= o.sequence {
		sequence = o.sequence + 1
	}
	o.sequence = sequence
	inv, projections, err := o.Collect(ctx)
	if err != nil {
		return controlplane.HostObservation{}, err
	}
	inv.AgentID, inv.SessionID, inv.Sequence = o.AgentID, request.SessionID, sequence
	inv.ObservedAt = now
	for i := range projections {
		projections[i].ObservedAt = now
		projections[i].AgentID, projections[i].SessionID, projections[i].Sequence = o.AgentID, request.SessionID, sequence
	}
	return controlplane.SignHostObservation(o.Key, controlplane.HostObservation{AgentID: o.AgentID, SessionID: request.SessionID, Sequence: sequence, BootID: o.BootID, ObservedAt: now, Inventory: inv, Projections: projections})
}

func (c *TaskClient) HostObservation(ctx context.Context, request ObservationRequest) (controlplane.HostObservation, error) {
	r, err := c.call(ctx, Request{Operation: "host-observe", Observation: &request})
	if err != nil {
		return controlplane.HostObservation{}, err
	}
	if r.Observation == nil {
		return controlplane.HostObservation{}, fmt.Errorf("host observation missing")
	}
	return *r.Observation, nil
}
