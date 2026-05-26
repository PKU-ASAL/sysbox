package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/state"
)

// Server holds all API state and registers HTTP routes.
type Server struct {
	runsDir       string
	workspacesDir string
	stateBackend  string
	cfg           config.ServiceConfig
	apiStore      apiStore
	agents        *agentRegistry
	jobs          *Jobs
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
	runsDir := cfg.Paths.RunsDir
	workspacesDir := cfg.Paths.WorkspacesDir
	if runsDir == "" {
		runsDir = config.DefaultRunsDir()
	}
	if workspacesDir == "" {
		workspacesDir = config.DefaultWorkspacesDir()
	}
	stateBackend := cfg.State.Backend
	apiStore := newAPIStore(runsDir, stateBackend)
	s := &Server{
		runsDir:       runsDir,
		workspacesDir: workspacesDir,
		stateBackend:  stateBackend,
		cfg:           cfg,
		apiStore:      apiStore,
		agents:        newAgentRegistry(),
		jobs:          newJobsWithPolicy(runsDir, apiStore, true, cfg.RunClaimTTL(), cfg.RunExpiredPolicy()),
		consoles:      newConsoleSessionHub(apiStore),
		nodeOps:       newNodeOperationStore(apiStore),
		mux:           http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) stateManager(topology string) (*state.Manager, error) {
	if s.stateBackend == "" {
		return state.NewManager(s.stateFile(topology)), nil
	}
	raw := strings.ReplaceAll(s.stateBackend, "{topology}", topology)
	b, err := state.ParseBackendURL(raw)
	if err != nil {
		return nil, fmt.Errorf("state backend: %w", err)
	}
	return state.NewManagerWithBackend(b), nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start binds to addr and serves until the process exits.
func (s *Server) Start(addr string) error {
	if addr == "" {
		addr = s.cfg.API.Listen
	}
	fmt.Fprintf(os.Stdout, "sysbox API listening on %s\n", addr)
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
		// WriteTimeout intentionally 0: SSE connections are long-lived.
	}
	return srv.ListenAndServe()
}

func (s *Server) registerRoutes() {
	m := s.mux

	m.HandleFunc("GET /v1/health", s.handleHealth)
	m.HandleFunc("GET /v1/schema", s.handleSchema)
	m.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
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
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/renew", s.handleRenewAgentRun)
	m.HandleFunc("POST /v1/agents/{agent}/runs/{id}/complete", s.handleCompleteAgentRun)
	m.HandleFunc("GET /v1/agents/{agent}/commands/stream", s.handleAgentCommandStream)
	m.HandleFunc("GET /v1/agents/{agent}/sessions/{session}/attach", s.handleAgentAttachConsole)
	m.HandleFunc("GET /v1/artifacts", s.handleListArtifacts)
	m.HandleFunc("GET /v1/policies", s.handleListPolicies)
	m.HandleFunc("POST /v1/policies", s.handleCreatePolicy)

	// Topologies
	m.HandleFunc("GET /v1/topologies", s.handleListTopologies)
	m.HandleFunc("POST /v1/topologies", s.handleCreateTopology)
	m.HandleFunc("GET /v1/topologies/{topology}", s.handleGetTopology)
	m.HandleFunc("GET /v1/topologies/{topology}/hcl", s.handleGetHCL)
	m.HandleFunc("PUT /v1/topologies/{topology}/hcl", s.handleUpdateHCL)
	m.HandleFunc("GET /v1/topologies/{topology}/state", s.handleGetState)
	m.HandleFunc("GET /v1/topologies/{topology}/state/metadata", s.handleGetStateMetadata)
	m.HandleFunc("GET /v1/topologies/{topology}/state/lock", s.handleGetStateLock)
	m.HandleFunc("POST /v1/topologies/{topology}/state/force-unlock", s.handleForceUnlockState)
	m.HandleFunc("GET /v1/topologies/{topology}/state/snapshots", s.handleListStateSnapshots)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore", s.handleRestoreStateSnapshot)
	m.HandleFunc("GET /v1/topologies/{topology}/stack-state", s.handleGetStackState)
	m.HandleFunc("GET /v1/topologies/{topology}/lease", s.handleGetWorkspaceLease)
	m.HandleFunc("GET /v1/topologies/{topology}/snapshots", s.handleListWorkspaceSnapshots)
	m.HandleFunc("GET /v1/topologies/{topology}/revisions", s.handleListRevisions)
	m.HandleFunc("POST /v1/topologies/{topology}/revisions", s.handleCreateRevision)
	m.HandleFunc("GET /v1/topologies/{topology}/revisions/{revision}", s.handleGetRevision)
	m.HandleFunc("GET /v1/topologies/{topology}/plans", s.handleListPlans)
	m.HandleFunc("POST /v1/topologies/{topology}/plans", s.handleCreatePlan)
	m.HandleFunc("GET /v1/topologies/{topology}/plans/{plan}", s.handleGetStoredPlan)
	m.HandleFunc("GET /v1/topologies/{topology}/outputs", s.handleGetOutputs)
	m.HandleFunc("GET /v1/topologies/{topology}/health", s.handleGetTopologyHealth)
	m.HandleFunc("GET /v1/topologies/{topology}/plan", s.handleGetPlan)
	m.HandleFunc("GET /v1/topologies/{topology}/graph", s.handleGetGraph)
	m.HandleFunc("GET /v1/topologies/{topology}/preflight", s.handlePreflight)
	m.HandleFunc("POST /v1/topologies/{topology}/apply", s.handleApply)
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
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/pause", s.handleNodePause)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/resume", s.handleNodeResume)
	m.HandleFunc("POST /v1/topologies/{topology}/import", s.handleImport)
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
