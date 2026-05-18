package api

import (
	"fmt"
	"net/http"
	"os"
)

// Server holds all API state and registers HTTP routes.
type Server struct {
	runsDir string
	jobs    *Jobs
	mux     *http.ServeMux
}

// NewServer creates a Server rooted at runsDir (e.g. "runs").
func NewServer(runsDir string) *Server {
	s := &Server{
		runsDir: runsDir,
		jobs:    newJobs(runsDir),
		mux:     http.NewServeMux(),
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
	return http.ListenAndServe(addr, authMiddleware(s))
}

func (s *Server) registerRoutes() {
	m := s.mux

	m.HandleFunc("GET /v1/health", s.handleHealth)

	// Topologies
	m.HandleFunc("GET /v1/topologies", s.handleListTopologies)
	m.HandleFunc("GET /v1/topologies/{suite}/state", s.handleGetState)
	m.HandleFunc("GET /v1/topologies/{suite}/plan", s.handleGetPlan)
	m.HandleFunc("POST /v1/topologies/{suite}/apply", s.handleApply)
	m.HandleFunc("POST /v1/topologies/{suite}/destroy", s.handleDestroy)

	// Runs (async job tracking + SSE logs)
	m.HandleFunc("GET /v1/runs/{id}", s.handleGetRun)
	m.HandleFunc("GET /v1/runs/{id}/logs", s.handleRunLogs)

	// Nodes
	m.HandleFunc("GET /v1/topologies/{suite}/nodes", s.handleListNodes)
	m.HandleFunc("GET /v1/topologies/{suite}/nodes/{node}", s.handleGetNode)
	m.HandleFunc("POST /v1/topologies/{suite}/nodes/{node}/exec", s.handleNodeExec)
}

// authMiddleware enforces SYSBOX_API_TOKEN when set.
func authMiddleware(next http.Handler) http.Handler {
	token := os.Getenv("SYSBOX_API_TOKEN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
