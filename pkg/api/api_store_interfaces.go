package api

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

type schemaStore interface {
	SchemaVersion(ctx context.Context) (int, error)
}

type runStore interface {
	LoadRuns(ctx context.Context) ([]controlplane.Run, error)
	GetRun(ctx context.Context, id string) (*controlplane.Run, error)
	SaveRun(ctx context.Context, run controlplane.Run) error
	ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error)
	RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error)
}

type checkpointStore interface {
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error
	LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error)
}

type healthStore interface {
	SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error
	LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error)
}

type revisionStore interface {
	SaveRevision(ctx context.Context, rev controlplane.Revision) error
	ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error)
	GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error)
}

type planStore interface {
	SavePlan(ctx context.Context, plan controlplane.Plan) error
	ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error)
	GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error)
}

type policyStore interface {
	SavePolicy(ctx context.Context, policy controlplane.Policy) error
	ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error)
}

type consoleStore interface {
	SaveConsoleSession(ctx context.Context, sess controlplane.ConsoleSession) error
	GetConsoleSession(ctx context.Context, id string) (*controlplane.ConsoleSession, error)
	ListConsoleSessions(ctx context.Context, workspace string) ([]controlplane.ConsoleSession, error)
}

type nodeOperationPersistence interface {
	SaveNodeOperation(ctx context.Context, op controlplane.NodeOperation) error
	GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error)
	ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error)
}

type agentStore interface {
	SaveAgent(ctx context.Context, agent controlplane.Agent) error
	GetAgent(ctx context.Context, id string) (*controlplane.Agent, error)
	ListAgents(ctx context.Context) ([]controlplane.Agent, error)
}

type agentCommandStore interface {
	SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error
	ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error)
	SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error
	ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error)
	AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error)
}

type inventoryStore interface {
	SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error
	GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error)
}

type apiStore interface {
	schemaStore
	runStore
	checkpointStore
	healthStore
	revisionStore
	planStore
	policyStore
	consoleStore
	nodeOperationPersistence
	agentStore
	agentCommandStore
	inventoryStore
}
