// Package controlplane defines sysbox product-level objects.
package controlplane

import (
	"time"

	"github.com/oslab/sysbox/pkg/state"
)

const DefaultProjectID = "default"
const AgentProtocolVersion = "agent.v1"

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type Workspace struct {
	ProjectID      string    `json:"project_id"`
	ArtifactID     string    `json:"artifact_id,omitempty"`
	TopologyID     string    `json:"topology_id,omitempty"`
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
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Workspace   string          `json:"workspace"`
	Revision    string          `json:"revision,omitempty"`
	StateSerial int64           `json:"state_serial,omitempty"`
	Fingerprint PlanFingerprint `json:"fingerprint"`
	Status      string          `json:"status"`
	Summary     string          `json:"summary,omitempty"`
	Actions     []PlannedChange `json:"actions"`
	CreatedAt   time.Time       `json:"created_at"`
}

type PlanFingerprint struct {
	ConfigSHA256    string            `json:"config_sha256"`
	StateLineage    string            `json:"state_lineage"`
	StateSerial     int64             `json:"state_serial"`
	ResourceSchemas map[string]int    `json:"resource_schemas"`
	Drivers         map[string]string `json:"drivers"`
	Artifacts       map[string]string `json:"artifacts"`
	VariablesSHA256 string            `json:"variables_sha256"`
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
	Protocol    string    `json:"protocol,omitempty"`
	LeaseOwner  string    `json:"lease_owner,omitempty"`
	LeaseUntil  time.Time `json:"lease_until,omitempty"`
	Attempt     int       `json:"attempt,omitempty"`
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
	Disabled      bool              `json:"disabled,omitempty"`
	Quarantined   bool              `json:"quarantined,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	AuthSecret    string            `json:"auth_secret,omitempty"`
	SecretHash    string            `json:"secret_hash,omitempty"`
	Protocol      string            `json:"protocol,omitempty"`
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

type AgentInventory struct {
	AgentID      string            `json:"agent_id"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Topologies   []InventoryItem   `json:"topologies,omitempty"`
	Artifacts    []InventoryItem   `json:"artifacts,omitempty"`
	Tools        []InventoryItem   `json:"tools,omitempty"`
	Status       string            `json:"status,omitempty"`
	Stale        bool              `json:"stale,omitempty"`
	ObservedAt   time.Time         `json:"observed_at"`
}

type InventoryItem struct {
	Workspace     string `json:"workspace,omitempty"`
	Topology      string `json:"topology"`
	Backend       string `json:"backend,omitempty"`
	Serial        int64  `json:"serial,omitempty"`
	ResourceCount int    `json:"resource_count,omitempty"`
	Health        string `json:"health,omitempty"`
	Path          string `json:"path,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Available     bool   `json:"available,omitempty"`
}

type ConsoleSession struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	Topology    string    `json:"topology"`
	Node        string    `json:"node"`
	AgentID     string    `json:"agent_id"`
	Status      string    `json:"status"`
	Err         string    `json:"error,omitempty"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	Roles       []string  `json:"roles,omitempty"`
	Policy      string    `json:"policy,omitempty"`
	TTY         bool      `json:"tty"`
	Audit       []Event   `json:"audit,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
}

type ResourceProjection struct {
	AgentID    string           `json:"agent_id,omitempty"`
	Workspace  string           `json:"workspace,omitempty"`
	Topology   string           `json:"topology,omitempty"`
	Serial     int64            `json:"serial,omitempty"`
	ObservedAt time.Time        `json:"observed_at,omitempty"`
	Health     TopologyHealth   `json:"health"`
	Resources  []ResourceHealth `json:"resources,omitempty"`
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
	RequestedBy    string            `json:"requested_by,omitempty"`
	Roles          []string          `json:"roles,omitempty"`
	Policy         string            `json:"policy,omitempty"`
}

type ConsoleCommand struct {
	Type    string          `json:"type"`
	Session *ConsoleSession `json:"session,omitempty"`
	Request ConsoleRequest  `json:"request,omitempty"`
}

type AgentCommand struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id,omitempty"`
	Type       string          `json:"type"`
	Status     string          `json:"status,omitempty"`
	Err        string          `json:"error,omitempty"`
	Protocol   string          `json:"protocol,omitempty"`
	Run        *Run            `json:"run,omitempty"`
	Session    *ConsoleSession `json:"session,omitempty"`
	Request    ConsoleRequest  `json:"request,omitempty"`
	Operation  NodeOperation   `json:"operation,omitempty"`
	CreatedAt  time.Time       `json:"created_at,omitempty"`
	Delivered  time.Time       `json:"delivered_at,omitempty"`
	AckedAt    time.Time       `json:"acked_at,omitempty"`
	EndedAt    time.Time       `json:"ended_at,omitempty"`
	LeaseOwner string          `json:"lease_owner,omitempty"`
	LeaseUntil time.Time       `json:"lease_until,omitempty"`
	Attempt    int             `json:"attempt,omitempty"`
}

type AgentCommandEvent struct {
	CommandID string    `json:"command_id,omitempty"`
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"`
	Status    string    `json:"status,omitempty"`
	Message   string    `json:"message,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type NodeOperation struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	Topology    string    `json:"topology"`
	Operation   string    `json:"operation"`
	Node        string    `json:"node,omitempty"`
	Type        string    `json:"type,omitempty"`
	Name        string    `json:"name,omitempty"`
	ExternalID  string    `json:"external_id,omitempty"`
	Substrate   string    `json:"substrate,omitempty"`
	AgentID     string    `json:"agent_id"`
	Status      string    `json:"status"`
	Err         string    `json:"error,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	Roles       []string  `json:"roles,omitempty"`
	Audit       []Event   `json:"audit,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
}

type NodeOperationCommand struct {
	Type      string        `json:"type"`
	Operation NodeOperation `json:"operation"`
}

func (op NodeOperation) Resource() string {
	if op.Type != "" && op.Name != "" {
		return op.Type + "." + op.Name
	}
	if op.Node != "" {
		return "sysbox_node." + op.Node
	}
	return ""
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
	Actor     string    `json:"actor,omitempty"`
	Roles     []string  `json:"roles,omitempty"`
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
