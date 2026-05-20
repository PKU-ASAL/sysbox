package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/oslab/sysbox/pkg/config"
)

// Server holds all API state and registers HTTP routes.
type Server struct {
	runsDir       string
	workspacesDir string
	jobs          *Jobs
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
	s := &Server{
		runsDir:       runsDir,
		workspacesDir: workspacesDir,
		jobs:          newJobs(runsDir),
		mux:           http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Start binds to addr and serves until the process exits.
func (s *Server) Start(addr string) error {
	fmt.Fprintf(os.Stdout, "sysbox API listening on %s\n", addr)
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
	m.HandleFunc("GET /v1/capabilities", s.handleCapabilities)

	// Topologies
	m.HandleFunc("GET /v1/topologies", s.handleListTopologies)
	m.HandleFunc("POST /v1/topologies", s.handleCreateTopology)
	m.HandleFunc("GET /v1/topologies/{topology}", s.handleGetTopology)
	m.HandleFunc("GET /v1/topologies/{topology}/hcl", s.handleGetHCL)
	m.HandleFunc("PUT /v1/topologies/{topology}/hcl", s.handleUpdateHCL)
	m.HandleFunc("GET /v1/topologies/{topology}/state", s.handleGetState)
	m.HandleFunc("GET /v1/topologies/{topology}/state/metadata", s.handleGetStateMetadata)
	m.HandleFunc("GET /v1/topologies/{topology}/state/snapshots", s.handleListStateSnapshots)
	m.HandleFunc("POST /v1/topologies/{topology}/state/snapshots/{snapshot}/restore", s.handleRestoreStateSnapshot)
	m.HandleFunc("GET /v1/topologies/{topology}/plan", s.handleGetPlan)
	m.HandleFunc("GET /v1/topologies/{topology}/graph", s.handleGetGraph)
	m.HandleFunc("GET /v1/topologies/{topology}/preflight", s.handlePreflight)
	m.HandleFunc("POST /v1/topologies/{topology}/apply", s.handleApply)
	m.HandleFunc("POST /v1/topologies/{topology}/destroy", s.handleDestroy)
	m.HandleFunc("DELETE /v1/topologies/{topology}", s.handleDeleteTopology)

	// Runs (async job tracking + SSE logs)
	m.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	m.HandleFunc("GET /v1/runs/{id}/checkpoint", s.handleGetRunCheckpoint)
	m.HandleFunc("GET /v1/runs/{id}/logs", s.handleRunLogs)

	// Nodes
	m.HandleFunc("GET /v1/topologies/{topology}/nodes", s.handleListNodes)
	m.HandleFunc("GET /v1/topologies/{topology}/nodes/{node}", s.handleGetNode)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/exec", s.handleNodeExec)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/pause", s.handleNodePause)
	m.HandleFunc("POST /v1/topologies/{topology}/nodes/{node}/resume", s.handleNodeResume)
	m.HandleFunc("POST /v1/topologies/{topology}/import", s.handleImport)
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
