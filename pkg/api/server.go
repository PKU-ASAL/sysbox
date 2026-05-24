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
	apiStore      apiStore
	jobs          *Jobs
	supervisor    *Supervisor
	mux           *http.ServeMux
}

// NewServer creates a Server rooted at runsDir (state files) and
// workspacesDir (HCL files). Empty values use the service data layout under
// SYSBOX_HOME.
func NewServer(runsDir, workspacesDir string) *Server {
	if workspacesDir == "" {
		workspacesDir = config.DefaultWorkspacesDir()
	}
	if runsDir == "" {
		runsDir = config.DefaultRunsDir()
	}
	stateBackend := os.Getenv("SYSBOX_STATE_BACKEND")
	apiStore := newAPIStore(runsDir, stateBackend)
	s := &Server{
		runsDir:       runsDir,
		workspacesDir: workspacesDir,
		stateBackend:  stateBackend,
		apiStore:      apiStore,
		jobs:          newJobs(runsDir, apiStore),
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
	fmt.Fprintf(os.Stdout, "sysbox API listening on %s\n", addr)
	if s.supervisor == nil {
		s.supervisor = newSupervisor(s, supervisorIntervalFromEnv())
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

func supervisorIntervalFromEnv() time.Duration {
	raw := os.Getenv("SYSBOX_SUPERVISOR_INTERVAL")
	if raw == "" {
		raw = "30s"
	}
	if raw == "0" || raw == "off" || raw == "disabled" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func (s *Server) registerRoutes() {
	m := s.mux

	m.HandleFunc("GET /v1/health", s.handleHealth)
	m.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
	m.HandleFunc("GET /v1/projects", s.handleListProjects)
	m.HandleFunc("GET /v1/projects/{project}", s.handleGetProject)
	m.HandleFunc("GET /v1/projects/{project}/workspaces", s.handleListProjectWorkspaces)
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

	// Nodes
	m.HandleFunc("GET /v1/topologies/{topology}/nodes", s.handleListNodes)
	m.HandleFunc("GET /v1/topologies/{topology}/nodes/{node}", s.handleGetNode)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/exec", s.handleNodeExec)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/pause", s.handleNodePause)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/resume", s.handleNodeResume)
	m.HandleFunc("POST /v1/topologies/{topology}/import", s.handleImport)
	m.HandleFunc("GET /v1/topologies/{topology}/resources", s.handleListResources)
	m.HandleFunc("GET /v1/topologies/{topology}/resources/{resource}/health", s.handleGetResourceHealth)
}

// authMiddleware enforces SYSBOX_API_TOKEN when set.
// Uses constant-time comparison to mitigate timing side-channel attacks.
func authMiddleware(next http.Handler) http.Handler {
	token := os.Getenv("SYSBOX_API_TOKEN")
	expectedPrefix := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := r.Header.Get("Authorization")
			if len(got) != len(expectedPrefix) || subtle.ConstantTimeCompare([]byte(got), []byte(expectedPrefix)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
