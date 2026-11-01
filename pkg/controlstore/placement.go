package controlstore

import (
	"context"
	"time"
)

type PlacementStore interface {
	GetTopologyPlacement(context.Context, string) (*Placement, error)
	SaveTopologyPlacement(context.Context, Placement) error
	DeleteTopologyPlacement(context.Context, string) error
}

// topologyPlacement is the durable host affinity for host-local resources.
type Placement struct {
	Topology string    `json:"topology"`
	AgentID  string    `json:"agent_id"`
	Protocol string    `json:"protocol"`
	BoundAt  time.Time `json:"bound_at"`
}
