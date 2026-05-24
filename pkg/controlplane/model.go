// Package controlplane defines sysbox product-level objects.
package controlplane

import (
	"time"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

const DefaultProjectID = "default"

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type Workspace struct {
	ProjectID      string    `json:"project_id"`
	Name           string    `json:"name"`
	HasHCL         bool      `json:"has_hcl"`
	HasState       bool      `json:"has_state"`
	ResourceCount  int       `json:"resource_count,omitempty"`
	Serial         int64     `json:"serial,omitempty"`
	Backend        string    `json:"backend,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	LatestRevision string    `json:"latest_revision,omitempty"`
}

type Revision struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Workspace   string    `json:"workspace"`
	Source      string    `json:"source"`
	SHA256      string    `json:"sha256"`
	Size        int       `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	Description string    `json:"description,omitempty"`
}

type Plan struct {
	ID          string               `json:"id"`
	ProjectID   string               `json:"project_id"`
	Workspace   string               `json:"workspace"`
	Revision    string               `json:"revision,omitempty"`
	StateSerial int64                `json:"state_serial,omitempty"`
	Status      string               `json:"status"`
	Summary     string               `json:"summary,omitempty"`
	Actions     []runtime.PlanAction `json:"actions"`
	CreatedAt   time.Time            `json:"created_at"`
}

type Run struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Workspace string    `json:"workspace"`
	Operation string    `json:"operation"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

type StackState struct {
	ProjectID string         `json:"project_id"`
	Workspace string         `json:"workspace"`
	Metadata  state.Metadata `json:"metadata"`
	State     *state.State   `json:"state,omitempty"`
}

type Event struct {
	RunID     string    `json:"run_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Resource  string    `json:"resource,omitempty"`
	Action    string    `json:"action,omitempty"`
	Status    string    `json:"status,omitempty"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type Artifact struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind,omitempty"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256,omitempty"`
	Size      int64     `json:"size,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type Lease struct {
	ProjectID string         `json:"project_id"`
	Workspace string         `json:"workspace"`
	Lock      state.LockInfo `json:"lock"`
}

type Policy struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Workspace   string    `json:"workspace,omitempty"`
	Mode        string    `json:"mode"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Snapshot struct {
	ProjectID string         `json:"project_id"`
	Workspace string         `json:"workspace"`
	Snapshot  state.Snapshot `json:"snapshot"`
}
