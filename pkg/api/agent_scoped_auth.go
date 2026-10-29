package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
)

// Agent authentication grants access only to its operational protocol. It
// cannot enroll another Agent, change policy, or submit topology mutations.
func (s *Server) authenticateAgentProtocol(w http.ResponseWriter, r *http.Request) bool {
	_, pattern := s.mux.Handler(r)
	allowed := false
	switch pattern {
	case "POST /v1/agents",
		"POST /v1/agents/{agent}/heartbeat", "POST /v1/agents/{agent}/observations",
		"POST /v1/agents/{agent}/execution-session", "GET /v1/agents/{agent}/commands/stream",
		"GET /v1/agents/{agent}/sessions/{session}/attach",
		"POST /v1/agents/{agent}/runs/{id}/claim", "POST /v1/agents/{agent}/runs/{id}/renew",
		"POST /v1/agents/{agent}/runs/{id}/revision",
		"POST /v1/agents/{agent}/guest-executions/{execution}/start",
		"POST /v1/agents/{agent}/guest-file-operations/{operation}/start",
		"GET /v1/agents/{agent}/guest-files/{file}", "POST /v1/agents/{agent}/guest-task-stops",
		"POST /v1/agents/{agent}/commands/{command}/execution-grant", "POST /v1/agents/{agent}/commands/{command}/execution-grant/authorize", "POST /v1/agents/{agent}/commands/{command}/execution-grant/renew",
		"POST /v1/agents/{agent}/execution-grants/authorize", "POST /v1/agents/{agent}/execution-grants/revoke", "POST /v1/agents/{agent}/execution-grants/inspect",
		"POST /v1/agents/{agent}/execution-grant/derive-guest", "POST /v1/agents/{agent}/execution-grant/derive-ssh", "POST /v1/agents/{agent}/execution-grant/renew-guest",
		"POST /v1/agents/{agent}/commands/{command}/recovery-authorize",
		"POST /v1/agents/{agent}/adoption-fences/{fence}/authorize", "POST /v1/agents/{agent}/gc-fences/{fence}/authorize",
		"POST /v1/agents/{agent}/gc-cleanup-proof", "POST /v1/agents/{agent}/gc-claim", "POST /v1/agents/{agent}/gc-grant",
		"POST /v1/agents/{agent}/ownership-transfers/authorize":
		allowed = true
	// These endpoints authenticate signed host evidence in their handlers.
	case "POST /v1/hosts/{agent}/execution-grants/stopped", "POST /v1/hosts/{agent}/runs/{run}/completed", "POST /v1/hosts/{agent}/node-operations/{operation}/completed", "POST /v1/hosts/{agent}/derived-ssh/{operation}/completed":
		return true
	}
	if !allowed {
		return false
	}
	id := r.Header.Get(agent.HeaderAgentID)
	if id == "" || controlstore.ValidatePathSegment(id, "agent") != nil {
		return false
	}
	stored, err := s.apiStore.GetAgent(r.Context(), id)
	if err != nil || stored == nil || stored.SecretHash == "" || stored.AuthSecret == "" {
		return false
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	}
	if err = s.verifyAgentRequest(r, id); err != nil {
		return false
	}
	if pattern == "POST /v1/agents" {
		raw, err := readAndRestoreBody(r)
		if err != nil {
			return false
		}
		var report controlplane.Agent
		return json.Unmarshal(raw, &report) == nil && report.ID == id
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) >= 3 && parts[2] == id
}
