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
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	Topology    string    `json:"topology"`
	Operation   string    `json:"operation,omitempty"`
	Op          string    `json:"op,omitempty"`
	Status      RunStatus `json:"status"`
	Err         string    `json:"error,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Revision    string    `json:"revision,omitempty"`
	PlanID      string    `json:"plan_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	Recoverable bool      `json:"recoverable,omitempty"`
	LeaseOwner  string    `json:"lease_owner,omitempty"`
	QueuedAt    time.Time `json:"queued_at,omitempty"`
	AssignedAt  time.Time `json:"assigned_at,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
}

type RunCompletion struct {
	Run        Run        `json:"run"`
	Projection Projection `json:"projection,omitempty"`
}

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunAssigned  RunStatus = "assigned"
	RunRunning   RunStatus = "running"
	RunDone      RunStatus = "done"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type Agent struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Status        string            `json:"status"`
	Capabilities  []string          `json:"capabilities,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Version       string            `json:"version,omitempty"`
	LastHeartbeat time.Time         `json:"last_heartbeat,omitempty"`
	CreatedAt     time.Time         `json:"created_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at,omitempty"`
}

type Projection struct {
	AgentID       string    `json:"agent_id,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	Topology      string    `json:"topology,omitempty"`
	Backend       string    `json:"backend,omitempty"`
	Serial        int64     `json:"serial,omitempty"`
	ResourceCount int       `json:"resource_count,omitempty"`
	Health        string    `json:"health,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type ConsoleSession struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Topology  string    `json:"topology"`
	Node      string    `json:"node"`
	AgentID   string    `json:"agent_id"`
	Status    string    `json:"status"`
	Err       string    `json:"error,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

type ConsoleRequest struct {
	Cmd            []string          `json:"cmd,omitempty"`
	Shell          string            `json:"shell,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	WorkDir        string            `json:"work_dir,omitempty"`
	TTY            *bool             `json:"tty,omitempty"`
	Cols           int               `json:"cols,omitempty"`
	Rows           int               `json:"rows,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type ConsoleCommand struct {
	Type    string          `json:"type"`
	Session *ConsoleSession `json:"session,omitempty"`
	Request ConsoleRequest  `json:"request,omitempty"`
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
