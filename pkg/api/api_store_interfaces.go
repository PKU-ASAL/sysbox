package api

import (
	"context"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
)

type schemaStore interface {
	SchemaVersion(ctx context.Context) (int, error)
}

type runStore interface {
	CompleteRunFromHost(context.Context, string, string, controlplane.SignedExecutionStopReceipt, controlplane.Run) (*controlplane.Run, error)
	CompleteRun(context.Context, string, string, controlplane.RunCompletion) (*controlplane.Run, bool, error)
	LoadRuns(ctx context.Context) ([]controlplane.Run, error)
	GetRun(ctx context.Context, id string) (*controlplane.Run, error)
	SaveRun(ctx context.Context, run controlplane.Run) error
	RestartRun(ctx context.Context, run controlplane.Run) (*controlplane.Run, error)
	ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error)
	RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error)
	ExpireRunLease(ctx context.Context, runID string, observedAt time.Time) (*controlplane.Run, bool, error)
	GetRunDispatch(ctx context.Context, requestID, fingerprint string) (*controlplane.Run, bool, error)
	CreateRunDispatch(ctx context.Context, request RunDispatchRequest) (*controlplane.Run, bool, error)
}

type RunDispatchRequest struct {
	Run         controlplane.Run
	Command     controlplane.AgentCommand
	Fingerprint string
}

type checkpointStore interface {
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error
	LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error)
}

type healthStore interface {
	SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error
	LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error)
	DeleteHealth(ctx context.Context, topology string) error
}

type projectionStore interface {
	SaveResourceProjection(ctx context.Context, proj controlplane.ResourceProjection) error
	LoadResourceProjection(ctx context.Context, topology string) (*controlplane.ResourceProjection, error)
}

type globalRevisionStore interface {
	SaveGlobalRevision(ctx context.Context, rev controlplane.GlobalRevision) error
	GetGlobalRevision(ctx context.Context, revision string) (*controlplane.GlobalRevision, error)
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
	CompleteAdoptionFromHost(context.Context, string, string, controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error)
	CompleteSSHAccessFromHost(context.Context, string, string, controlplane.SignedExecutionStopReceipt) (*controlplane.NodeOperation, error)
	CompareAndSwapNodeOperation(context.Context, controlplane.NodeOperation, controlplane.NodeOperation) error
	CreateNodeOperation(ctx context.Context, op controlplane.NodeOperation) error
	CreateNodeOperationDispatch(context.Context, controlplane.NodeOperation, controlplane.AgentCommand) error
	CompleteNodeOperation(context.Context, string, string, controlplane.NodeOperation) (*controlplane.NodeOperation, error)
	GetNodeOperation(ctx context.Context, id string) (*controlplane.NodeOperation, error)
	ListNodeOperations(ctx context.Context, workspace string) ([]controlplane.NodeOperation, error)
}

type agentStore interface {
	RegisterAgent(context.Context, controlplane.Agent) (*controlplane.Agent, error)
	HeartbeatAgent(context.Context, controlplane.Agent) (*controlplane.Agent, error)
	UpdateAgentPolicy(context.Context, string, persistence.AgentPolicy) (*controlplane.Agent, error)
	ExpireAgentHeartbeat(context.Context, string, time.Time, time.Duration) (*controlplane.Agent, error)
	SaveAgent(ctx context.Context, agent controlplane.Agent) error
	GetAgent(ctx context.Context, id string) (*controlplane.Agent, error)
	ListAgents(ctx context.Context) ([]controlplane.Agent, error)
}

type agentCommandStore interface {
	GetRunRecoveryReport(context.Context, string, string) (*controlplane.RunRecoveryReport, error)
	ApplyAgentCommandEvent(context.Context, controlplane.AgentCommandEvent) (*controlplane.AgentCommand, error)
	LoadDispatchRetry(context.Context, string) (DispatchRetryRecord, error)
	SaveDispatchRetry(context.Context, DispatchRetryRecord) error
	ClearDispatchRetry(context.Context, string) error
	PrepareAgentCommandDelivery(context.Context, controlplane.AgentCommand) (*controlplane.AgentCommand, error)
	SaveAgentCommandEvent(ctx context.Context, event controlplane.AgentCommandEvent) error
	ListAgentCommandEvents(ctx context.Context, agentID string) ([]controlplane.AgentCommandEvent, error)
	SaveAgentCommand(ctx context.Context, cmd controlplane.AgentCommand) error
	DeleteAgentCommand(ctx context.Context, agentID, commandID string) error
	ListAgentCommands(ctx context.Context, agentID string) ([]controlplane.AgentCommand, error)
	AcquireAgentCommandLease(ctx context.Context, agentID, commandID, owner string, ttl time.Duration) (*controlplane.AgentCommand, bool, error)
}

type inventoryStore interface {
	SaveAgentInventory(ctx context.Context, inv controlplane.AgentInventory) error
	GetAgentInventory(ctx context.Context, agentID string) (*controlplane.AgentInventory, error)
}

type guestExecutionPersistence interface {
	SaveGuestExecution(ctx context.Context, execution controlplane.GuestExecution) error
	CreateGuestExecutionDispatch(ctx context.Context, execution controlplane.GuestExecution, command controlplane.AgentCommand) error
	CompareAndSwapGuestExecution(ctx context.Context, execution controlplane.GuestExecution, expectedVersion int64) (bool, error)
	GetGuestExecution(ctx context.Context, id string) (*controlplane.GuestExecution, error)
}

type guestFileOperationPersistence interface {
	CreateGuestFileDispatch(context.Context, controlplane.GuestFileOperation, controlplane.AgentCommand) error
	SaveGuestFileOperation(context.Context, controlplane.GuestFileOperation) error
	GetGuestFileOperation(context.Context, string) (*controlplane.GuestFileOperation, error)
	CompareAndSwapGuestFileOperation(context.Context, controlplane.GuestFileOperation, int64) (bool, error)
}

type guestAcceptancePersistence interface {
	LatestGuestAcceptance(context.Context, string) (time.Time, time.Time, error)
}

type apiStore interface {
	CompleteFileFromHost(context.Context, string, string, controlplane.SignedExecutionStopReceipt) (controlplane.GuestFileOperation, error)
	ListGuestActivity(context.Context, string) ([]persistence.GuestActivity, error)
	CompleteGuestExecution(context.Context, string, string, controlplane.GuestExecutionCompletion) (controlplane.GuestExecution, error)
	CompleteGuestFileOperation(context.Context, string, string, controlplane.GuestFileOperationCompletion) (controlplane.GuestFileOperation, error)
	schemaStore
	runStore
	checkpointStore
	healthStore
	projectionStore
	globalRevisionStore
	policyStore
	consoleStore
	nodeOperationPersistence
	agentStore
	agentCommandStore
	inventoryStore
	guestExecutionPersistence
	guestFileOperationPersistence
	guestAcceptancePersistence
}
