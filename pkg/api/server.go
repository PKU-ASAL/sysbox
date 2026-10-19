package api

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/state"
)

// Server holds all API state and registers HTTP routes.
type Server struct {
	fenceTargets  map[string]hostFenceTarget
	grantSigner   ed25519.PrivateKey
	grantHostKeys map[string]ed25519.PublicKey
	repositoryMu  sync.Mutex
	repositories  map[string]*persistence.Repository
	runsDir       string
	workspacesDir string
	stateBackend  string
	cfg           config.ServiceConfig
	apiStore      apiStore
	initErr       error
	agents        *agentRegistry
	agentSvc      *AgentService
	agentStream   *AgentStreamService
	jobs          *Jobs
	runService    *RunService
	scheduler     *SchedulerService
	workspaces    *WorkspaceService
	nodeService   *NodeOperationService
	consoleSvc    *ConsoleService
	guestExecSvc  *GuestExecutionService
	consoles      *consoleSessionHub
	nodeOps       *nodeOperationStore
	supervisor    *Supervisor
	mux           *http.ServeMux
}

// NewServer creates a Server rooted at runsDir (state files) and
// workspacesDir (HCL files). Empty values use the service data layout from
// sysbox.yaml.
func NewServer(runsDir, workspacesDir string) *Server {
	cfg := config.MustLoadServiceConfig("")
	if runsDir != "" {
		cfg.Paths.RunsDir = runsDir
	}
	if workspacesDir != "" {
		cfg.Paths.WorkspacesDir = workspacesDir
	}
	return NewServerWithConfig(cfg)
}

func NewServerWithConfig(cfg config.ServiceConfig) *Server {
	return newServerWithConfig(cfg, true)
}

// Embedded execution controllers share services and storage, but they are not
// API restarts and must not recover another process's live sessions or Runs.
func newServerWithConfig(cfg config.ServiceConfig, recoverInterrupted bool) *Server {
	runsDir := cfg.Paths.RunsDir
	workspacesDir := cfg.Paths.WorkspacesDir
	if runsDir == "" {
		runsDir = config.DefaultRunsDir()
	}
	if workspacesDir == "" {
		workspacesDir = config.DefaultWorkspacesDir()
	}
	stateBackend := cfg.State.Backend
	apiStore, initErr := newAPIStore(runsDir, stateBackend)
	if initErr != nil {
		// Keep construction safe for non-Start consumers; Start reports the
		// error so the API server refuses to bind.
		apiStore = &localAPIStore{runsDir: runsDir}
	}
	s := &Server{
		runsDir:       runsDir,
		workspacesDir: workspacesDir,
		stateBackend:  stateBackend,
		cfg:           cfg,
		apiStore:      apiStore,
		initErr:       initErr,
		agents:        newAgentRegistry(),
		jobs:          newJobsWithPolicy(runsDir, apiStore, recoverInterrupted, cfg.RunClaimTTL(), cfg.RunExpiredPolicy()),
		consoles:      newConsoleSessionHubWithRecovery(apiStore, recoverInterrupted),
		nodeOps:       newNodeOperationStore(apiStore),
		mux:           http.NewServeMux(),
	}
	if err := s.loadExecutionGrantSigner(); err != nil && s.initErr == nil {
		s.initErr = err
	}
	if s.initErr == nil && s.consoles.initErr != nil {
		s.initErr = fmt.Errorf("initialize console registry: %w", s.consoles.initErr)
	}
	if err := s.loadHostFenceTargets(); err != nil && s.initErr == nil {
		s.initErr = err
	}
	s.agentSvc = newAgentService(s)
	s.workspaces = newWorkspaceService(s.runsDir, s.workspacesDir, s.stateBackend, s.stateManager, s.apiStore)
	s.workspaces.gcSourceID = s.cfg.GCPublicationSource
	s.workspaces.gcRepository = s.actionRepository
	s.workspaces.loadRuns = s.apiStore.LoadRuns
	s.workspaces.loadNodeOperations = s.apiStore.ListNodeOperations
	s.workspaces.loadGuestActivity = s.apiStore.ListGuestActivity
	s.workspaces.loadConsoleSessions = s.apiStore.ListConsoleSessions
	s.workspaces.loadAgentCommands = s.loadMaintenanceCommands
	s.workspaces.deleteState = s.deleteStateWithAuthority
	s.workspaces.lockAdmission = s.jobs.lockTopology
	if p, ok := s.apiStore.(durablePlacementStore); ok {
		s.workspaces.placementStore = p
	}
	s.scheduler = newSchedulerService(s)
	s.nodeService = newNodeOperationService(s.workspaceService(), s.scheduling(), s.nodeOps, s.agentService().WakeCommand)
	defaultConsoleTimeout, _ := time.ParseDuration(s.cfg.API.Console.DefaultTimeout)
	maxConsoleTimeout, _ := time.ParseDuration(s.cfg.API.Console.MaxTimeout)
	s.consoleSvc = newConsoleService(
		s.consoles,
		s.scheduling(),
		s.agentService().WakeCommand,
		s.authorizeConsole,
		s.workspaceService().HCLFile,
		s.workspaceService().LoadState,
		s.authoritativeTopologyHealth,
		defaultConsoleTimeout,
		maxConsoleTimeout,
	)
	s.guestExecSvc = newGuestExecutionServiceFromStore(s.apiStore, s.agentService().PublishCommand, nil)
	s.agentStream = newAgentStreamService(s)
	s.runService = newRunService(s)
	s.registerRoutes()
	return s
}

func (s *Server) stateManager(topology string) (*state.Manager, error) {
	repo, err := s.actionRepository(topology)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(topology)), nil
}

func (s *Server) actionRepository(topology string) (*persistence.Repository, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	path := filepath.Join(s.runsDir, topology, "state.json")
	raw, path, err := persistence.ResolveLocation(s.stateBackend, path, topology)
	if err != nil {
		return nil, err
	}
	// A cached pool must not hide newly discovered legacy state corruption.
	if raw == "" {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, fmt.Errorf("%w: standalone state requires explicit rebuild", persistence.ErrUnsupportedVersion)
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}
	key := raw
	if key == "" {
		key = path
	}
	s.repositoryMu.Lock()
	defer s.repositoryMu.Unlock()
	if repo := s.repositories[key]; repo != nil {
		return repo, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return nil, err
	}
	if s.repositories == nil {
		s.repositories = map[string]*persistence.Repository{}
	}
	s.repositories[key] = repo
	return repo, nil
}

func (s *Server) runs() *RunService {
	if s.runService == nil {
		s.runService = newRunService(s)
	}
	return s.runService
}

func (s *Server) scheduling() *SchedulerService {
	if s.scheduler == nil {
		s.scheduler = newSchedulerService(s)
	}
	return s.scheduler
}

func (s *Server) workspaceService() *WorkspaceService {
	if s.workspaces == nil {
		s.workspaces = newWorkspaceService(s.runsDir, s.workspacesDir, s.stateBackend, s.stateManager, s.apiStore)
		s.workspaces.gcSourceID = s.cfg.GCPublicationSource
		s.workspaces.gcRepository = s.actionRepository
		s.workspaces.loadRuns = s.apiStore.LoadRuns
		s.workspaces.loadNodeOperations = s.apiStore.ListNodeOperations
		s.workspaces.loadGuestActivity = s.apiStore.ListGuestActivity
		s.workspaces.loadConsoleSessions = s.apiStore.ListConsoleSessions
		s.workspaces.loadAgentCommands = s.loadMaintenanceCommands
		s.workspaces.deleteState = s.deleteStateWithAuthority
		s.workspaces.lockAdmission = s.jobs.lockTopology
		if p, ok := s.apiStore.(durablePlacementStore); ok {
			s.workspaces.placementStore = p
		}
	}
	return s.workspaces
}

func (s *Server) loadMaintenanceCommands(ctx context.Context) ([]controlplane.AgentCommand, error) {
	return s.apiStore.ListAgentCommands(ctx, "")
}

func (s *Server) nodeOperations() *NodeOperationService {
	if s.nodeService == nil {
		s.nodeService = newNodeOperationService(s.workspaceService(), s.scheduling(), s.nodeOps, s.agentService().WakeCommand)
	}
	return s.nodeService
}

func (s *Server) consoleService() *ConsoleService {
	if s.consoleSvc == nil {
		defaultConsoleTimeout, _ := time.ParseDuration(s.cfg.API.Console.DefaultTimeout)
		maxConsoleTimeout, _ := time.ParseDuration(s.cfg.API.Console.MaxTimeout)
		s.consoleSvc = newConsoleService(
			s.consoles,
			s.scheduling(),
			s.agentService().WakeCommand,
			s.authorizeConsole,
			s.workspaceService().HCLFile,
			s.workspaceService().LoadState,
			s.authoritativeTopologyHealth,
			defaultConsoleTimeout,
			maxConsoleTimeout,
		)
	}
	return s.consoleSvc
}

func (s *Server) agentService() *AgentService {
	if s.agentSvc == nil {
		s.agentSvc = newAgentService(s)
	}
	return s.agentSvc
}

func (s *Server) agentStreams() *AgentStreamService {
	if s.agentStream == nil {
		s.agentStream = newAgentStreamService(s)
	}
	return s.agentStream
}

func (s *Server) guestExecutions() *GuestExecutionService {
	if s.guestExecSvc == nil {
		s.guestExecSvc = newGuestExecutionServiceFromStore(s.apiStore, s.agentService().PublishCommand, nil)
	}
	return s.guestExecSvc
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start binds to addr and serves until the process exits.
func (s *Server) Start(addr string) error {
	return s.StartContext(context.Background(), addr)
}

// StartContext serves until ctx is canceled, allowing up to five seconds for
// active requests to finish before closing their connections.
func (s *Server) StartContext(ctx context.Context, addr string) error {
	if s.initErr != nil {
		return s.initErr
	}
	if ctx.Err() != nil {
		return nil
	}
	if addr == "" {
		addr = s.cfg.API.Listen
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	fmt.Fprintf(os.Stdout, "sysbox API listening on %s\n", listener.Addr())
	if s.supervisor == nil {
		s.supervisor = newSupervisor(s, s.cfg.SupervisorInterval())
	}
	s.supervisor.Start()
	defer s.supervisor.Stop()
	srv := &http.Server{
		Addr:              addr,
		Handler:           authMiddleware(s),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		// WriteTimeout intentionally 0: SSE connections are long-lived.
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = srv.Close()
		}
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return shutdownErr
	}
}

func (s *Server) originPatterns() []string {
	origins := s.cfg.API.AllowedOrigins
	if len(origins) == 0 {
		return []string{"*"} // dev default: accept all origins
	}
	return origins
}

func (s *Server) registerRoutes() {
	m := s.mux
	s.registerExecutionGrantRoutes()

	m.HandleFunc("GET /v1/health", s.handleHealth)
	m.HandleFunc("GET /v1/schema", s.handleSchema)
	m.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
	m.HandleFunc("GET /v1/topologies/{topology}/cleanup-inspection", s.handleCleanupInspection)
	m.HandleFunc("GET /v1/topologies/{topology}/recovery/intent-only", s.handleHistoricalIntents)
	m.HandleFunc("POST /v1/evaluations", s.handleCreateEvaluation)
	m.HandleFunc("GET /v1/projects", s.handleListProjects)
	m.HandleFunc("GET /v1/projects/{project}", s.handleGetProject)
	m.HandleFunc("GET /v1/projects/{project}/workspaces", s.handleListProjectWorkspaces)
	m.HandleFunc("GET /v1/agents", s.handleListAgents)
	m.HandleFunc("POST /v1/agents", s.handleRegisterAgent)
	m.HandleFunc("GET /v1/agents/{agent}", s.handleGetAgent)
	m.HandleFunc("PATCH /v1/agents/{agent}", s.handleUpdateAgent)
	m.HandleFunc("POST /v1/agents/{agent}/heartbeat", s.handleAgentHeartbeat)
	m.HandleFunc("GET /v1/agents/{agent}/projections", s.handleListAgentProjections)
	m.HandleFunc("GET /v1/agents/{agent}/inventory", s.handleGetAgentInventory)
	m.HandleFunc("POST /v1/agents/{agent}/inventory", s.handlePostAgentInventory)
	m.HandleFunc("GET /v1/agents/{agent}/commands", s.handleListAgentCommands)
	m.HandleFunc("POST /v1/agents/{agent}/commands/{command}/cancel", s.handleCancelAgentCommand)
	m.HandleFunc("GET /v1/agents/{agent}/command-events", s.handleListAgentCommandEvents)
	m.HandleFunc("POST /v1/agents/{agent}/projections/resources", s.handlePostAgentResourceProjection)
	m.HandleFunc("POST /v1/agents/{agent}/node-operations/{operation}/complete", s.handleCompleteNodeOperation)
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/claim", s.handleClaimAgentRun)
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/revision", s.handleRunRevision)
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/renew", s.handleRenewAgentRun)
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/complete", s.handleCompleteAgentRun)
	m.HandleFunc("GET /v1/agents/{agent}/commands/stream", s.handleAgentCommandStream)
	m.HandleFunc("GET /v1/agents/{agent}/sessions/{session}/attach", s.handleAgentAttachConsole)
	m.HandleFunc("GET /v1/artifacts", s.handleListArtifacts)
	m.HandleFunc("GET /v1/policies", s.handleListPolicies)
	m.HandleFunc("POST /v1/policies", s.handleCreatePolicy)
	m.HandleFunc("POST /v1/revisions", s.handlePublishRevision)
	m.HandleFunc("POST /v1/revisions/retention-plan", s.handleRevisionRetention)
	m.HandleFunc("POST /v1/runs/{id}/remote/{mode}", s.handleRequestRunRecovery)
	m.HandleFunc("GET /v1/runs/{id}/remote-reports/{command}", s.handleGetRunRecovery)
	m.HandleFunc("POST /v1/agents/{agent}/commands/{command}/recovery-authorize", s.handleAuthorizeRunRecovery)

	// Topologies
	m.HandleFunc("POST /v1/topologies/{topology}/host-fences/{fence}/reconcile-delete", s.handleFenceDelete)
	m.HandleFunc("POST /v1/topologies/{topology}/host-fences/{fence}/release", s.handleHostFenceRelease)
	m.HandleFunc("GET /v1/topologies/{topology}/host-fences/{fence}/recovery-plan", s.handleHostFenceRecoveryPlan)
	m.HandleFunc("POST /v1/topologies/{topology}/host-fences/{fence}/observe", s.handleHostFenceObserve)
	m.HandleFunc("POST /v1/topologies/{topology}/host-fences", s.handleBeginHostFence)
	m.HandleFunc("GET /v1/topologies/{topology}/host-fences/{fence}", s.handleGetHostFence)
	m.HandleFunc("POST /v1/topologies/{topology}/host-fences/{fence}/receipt", s.handleRecordHostFence)
	m.HandleFunc("POST /v1/topologies/{topology}/adoption-inspections", s.handleAdoptionInspection)
	m.HandleFunc("POST /v1/topologies/{topology}/adoption-inspections/{inspection}/confirm", s.handleConfirmAdoptionInspection)
	m.HandleFunc("POST /v1/topologies/{topology}/gc", s.handleGC)
	m.HandleFunc("POST /v1/topologies/{topology}/gc/batch", s.handleGCBatch)
	m.HandleFunc("GET /v1/topologies/{topology}/gc/references", s.handleGCReferences)
	m.HandleFunc("GET /v1/topologies/{topology}/gc/sources", s.handleGCSources)
	m.HandleFunc("POST /v1/topologies/{topology}/gc/sources/{source}/{operation}", s.handleGCSourcePublication)
	m.HandleFunc("GET /v1/topologies", s.handleListTopologies)
	m.HandleFunc("GET /v1/topologies/{topology}", s.handleGetTopology)
	m.HandleFunc("GET /v1/topologies/{topology}/hcl", s.handleGetHCL)
	m.HandleFunc("GET /v1/topologies/{topology}/state", s.handleGetState)
	m.HandleFunc("GET /v1/topologies/{topology}/state/metadata", s.handleGetStateMetadata)
	m.HandleFunc("GET /v1/topologies/{topology}/state/lock", s.handleGetStateLock)
	m.HandleFunc("POST /v1/topologies/{topology}/state/force-unlock", s.handleForceUnlockState)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/retention-plan", s.handleSnapshotRetentionPlan)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/retention-apply", s.handleSnapshotRetentionApply)
	m.HandleFunc("GET /v1/topologies/{topology}/state/snapshots", s.handleListStateSnapshots)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots", s.handleCreateStateSnapshot)
	m.HandleFunc("DELETE /v1/topologies/{topology}/state/snapshots/{snapshot}", s.handleDeleteStateSnapshot)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore", s.handleRestoreStateSnapshot)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore-plan", s.handlePlanSnapshotRestore)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore-cancel", s.handleCancelSnapshotRestore)
	m.HandleFunc("GET /v1/topologies/{topology}/stack-state", s.handleGetStackState)
	m.HandleFunc("GET /v1/topologies/{topology}/lease", s.handleGetWorkspaceLease)
	m.HandleFunc("GET /v1/topologies/{topology}/snapshots", s.handleListWorkspaceSnapshots)
	m.HandleFunc("GET /v1/topologies/{topology}/outputs", s.handleGetOutputs)
	m.HandleFunc("GET /v1/topologies/{topology}/health", s.handleGetTopologyHealth)
	m.HandleFunc("GET /v1/topologies/{topology}/plan", s.handleGetPlan)
	m.HandleFunc("GET /v1/topologies/{topology}/graph", s.handleGetGraph)
	m.HandleFunc("GET /v1/topologies/{topology}/preflight", s.handlePreflight)
	m.HandleFunc("POST /v1/topologies/{topology}/apply", s.handleApply)
	m.HandleFunc("POST /v1/topologies/{topology}/repair", s.handleRepair)
	m.HandleFunc("POST /v1/topologies/{topology}/reset", s.handleReset)
	m.HandleFunc("POST /v1/topologies/{topology}/destroy", s.handleDestroy)
	m.HandleFunc("DELETE /v1/topologies/{topology}", s.handleDeleteTopology)

	// Runs (async job tracking + SSE logs)
	m.HandleFunc("GET /v1/runs", s.handleListRuns)
	m.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	m.HandleFunc("POST /v1/runs/{id}/resume", s.handleResumeRun)
	m.HandleFunc("POST /v1/runs/{id}/recover", s.handleRecoverRun)
	m.HandleFunc("POST /v1/runs/{id}/cleanup", s.handleCleanupRun)
	m.HandleFunc("GET /v1/runs/{id}/checkpoint", s.handleGetRunCheckpoint)
	m.HandleFunc("GET /v1/runs/{id}/actions", s.handleGetRunActions)
	m.HandleFunc("GET /v1/runs/{id}/events", s.handleListRunEvents)
	m.HandleFunc("GET /v1/runs/{id}/logs", s.handleRunLogs)

	// Console sessions
	m.HandleFunc("GET /v1/sessions/{session}", s.handleGetConsoleSession)
	m.HandleFunc("GET /v1/sessions/{session}/attach", s.handleAttachConsoleSession)
	m.HandleFunc("POST /v1/sessions/{session}/cancel", s.handleCancelConsoleSession)

	// Nodes
	m.HandleFunc("GET /v1/node-operations/{operation}", s.handleGetNodeOperation)
	m.HandleFunc("GET /v1/topologies/{topology}/nodes", s.handleListNodes)
	m.HandleFunc("GET /v1/topologies/{topology}/nodes/{node}", s.handleGetNode)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/sessions", s.handleCreateConsoleSession)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/executions", s.handleCreateGuestExecution)
	m.HandleFunc("GET /v1/executions/{execution}", s.handleGetGuestExecution)
	m.HandleFunc("POST /v1/executions/{execution}/cancel", s.handleCancelGuestExecution)
	m.HandleFunc("POST /v1/agents/{agent}/guest-executions/{execution}/start", s.handleStartGuestExecution)
	m.HandleFunc("POST /v1/agents/{agent}/guest-file-operations/{operation}/start", s.handleStartGuestFileOperation)
	m.HandleFunc("POST /v1/agents/{agent}/guest-file-operations/{operation}/complete", s.handleCompleteGuestFileOperation)
	m.HandleFunc("PUT /v1/topologies/{topology}/nodes/{node}/files", s.handlePutGuestFile)
	m.HandleFunc("GET /v1/file-operations/{operation}", s.handleGetGuestFileOperation)
	m.HandleFunc("POST /v1/file-operations/{operation}/cancel", s.handleCancelGuestFileOperation)
	m.HandleFunc("GET /v1/agents/{agent}/guest-files/{file}", s.handleFetchGuestFile)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/pause", s.handleNodePause)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/resume", s.handleNodeResume)
	m.HandleFunc("POST /v1/topologies/{topology}/import", s.handleImport)
	m.HandleFunc("POST /v1/topologies/{topology}/adoption-plan", s.handleAdoptionPlan)
	m.HandleFunc("POST /v1/topologies/{topology}/ssh-access", s.handleSSHAccess)
	m.HandleFunc("POST /v1/agents/{agent}/adoption-confirm", s.handleAdoptionConfirm)
	m.HandleFunc("GET /v1/topologies/{topology}/resources", s.handleListResources)
	m.HandleFunc("GET /v1/topologies/{topology}/status/stream", s.handleTopologyStatusStream)
	m.HandleFunc("GET /v1/topologies/{topology}/resources/{resource}/health", s.handleGetResourceHealth)
}

// authMiddleware enforces SYSBOX_API_TOKEN when set.
// Uses constant-time comparison to mitigate timing side-channel attacks.
func authMiddleware(next http.Handler) http.Handler {
	var token string
	if s, ok := next.(*Server); ok {
		token = s.cfg.API.Token
	}
	expectedPrefix := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := r.Header.Get("Authorization")
			if got == "" && r.URL.Query().Get("token") != "" {
				got = "Bearer " + r.URL.Query().Get("token")
			}
			if len(got) != len(expectedPrefix) || subtle.ConstantTimeCompare([]byte(got), []byte(expectedPrefix)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
