package api

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/substrate"
)

type preflightCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"` // error | warning | info
	Message  string `json:"message,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type preflightResult struct {
	OK     bool             `json:"ok"`
	Checks []preflightCheck `json:"checks"`
}

func (r *preflightResult) add(name string, ok bool, severity, message, hint string) {
	r.Checks = append(r.Checks, preflightCheck{
		Name:     name,
		OK:       ok,
		Severity: severity,
		Message:  message,
		Hint:     hint,
	})
	if !ok && severity == "error" {
		r.OK = false
	}
}

func (r *preflightResult) err() error {
	if r.OK {
		return nil
	}
	for _, c := range r.Checks {
		if !c.OK && c.Severity == "error" {
			if c.Hint != "" {
				return fmt.Errorf("preflight failed: %s: %s (%s)", c.Name, c.Message, c.Hint)
			}
			return fmt.Errorf("preflight failed: %s: %s", c.Name, c.Message)
		}
	}
	return fmt.Errorf("preflight failed")
}

// GET /v1/capabilities
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	res := preflightResult{OK: true}
	for _, name := range []string{"docker", "firecracker", "libvirt"} {
		if sub, err := substrate.Get(name); err == nil {
			addPreflightChecks(&res, sub.PreflightChecks(false))
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// GET /v1/topologies/{topology}/preflight
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	topology := r.PathValue("topology")
	if err := validatePathSegment(topology, "topology"); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.preflightTopology(topology)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) preflightTopology(topology string) (*preflightResult, error) {
	root, err := config.ParseFile(s.workspaceService().HCLFile(topology))
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	ctx := config.BuildEvalContext(root, filepath.Dir(s.workspaceService().HCLFile(topology)))
	res := &preflightResult{OK: true}

	needed := map[string]bool{}
	for _, sb := range root.Substrates {
		needed[sb.Type] = true
	}

	for name := range needed {
		if sub, err := substrate.Get(name); err == nil {
			addPreflightChecks(res, sub.PreflightChecks(true))
		}
	}

	for _, rb := range root.Resources {
		addPreflightChecks(res, runtime.ResourcePreflightChecks(rb, ctx))
	}

	return res, nil
}

func addPreflightChecks(res *preflightResult, checks []substrate.PreflightCheck) {
	for _, c := range checks {
		res.add(c.Name, c.OK, c.Severity, c.Message, c.Hint)
	}
}
