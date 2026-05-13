package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/oslab/sysbox/pkg/matcher"
	pkgstate "github.com/oslab/sysbox/pkg/state"
)

// Server is the sysbox hook HTTP server.
// It intercepts Claude Code PreToolUse / PostToolUse events and extracts
// IoC predictions from Bash commands before they execute.
type Server struct {
	extractor       Extractor
	predictionsPath string
	st              *pkgstate.State

	mu          sync.Mutex
	stepCounter map[string]int    // session_id → current step
	lastNode    map[string]string // session_id → last observed node
	runIDs      map[string]string // session_id → langfuse run_id
	stats       struct {
		predictionsWritten int
		requestsHandled    int
	}
}

// NewServer creates a hook server.
// st may be nil (node identification will be limited to SSH IP parsing only).
func NewServer(extractor Extractor, predictionsPath string, st *pkgstate.State) *Server {
	return &Server{
		extractor:       extractor,
		predictionsPath: predictionsPath,
		st:              st,
		stepCounter:     make(map[string]int),
		lastNode:        make(map[string]string),
		runIDs:          make(map[string]string),
	}
}

// Handler returns an http.Handler for all hook endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hooks/pre-tool-use", s.handlePreToolUse)
	mux.HandleFunc("/hooks/post-tool-use", s.handlePostToolUse)
	mux.HandleFunc("/hooks/session", s.handleSession)
	mux.HandleFunc("/hooks/status", s.handleStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return mux
}

// claudeHookInput is the common JSON structure Claude Code sends on every hook call.
type claudeHookInput struct {
	SessionID     string         `json:"session_id"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	ToolResponse  map[string]any `json:"tool_response"`
}

// handlePreToolUse extracts an IoC prediction before a Bash command runs.
func (s *Server) handlePreToolUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var input claudeHookInput
	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, "parse JSON", http.StatusBadRequest)
		return
	}

	command, _ := input.ToolInput["command"].(string)
	if command == "" || input.ToolName != "Bash" {
		// Non-Bash or empty command: allow immediately, no prediction.
		w.WriteHeader(http.StatusOK)
		return
	}

	s.mu.Lock()
	s.stats.requestsHandled++
	s.stepCounter[input.SessionID]++
	step := s.stepCounter[input.SessionID]
	runID := s.runIDs[input.SessionID]
	if runID == "" {
		runID = input.SessionID
	}
	s.mu.Unlock()

	// Resolve target node from the command.
	node := s.resolveNode(input.SessionID, command)

	call := ToolCall{
		ToolName:  "bash_exec",
		Command:   command,
		Node:      node,
		RunID:     runID,
		AgentStep: step,
	}

	pred := s.extractor.Extract(call)

	var contextMsg string
	if len(pred.ExpectedEvents) > 0 && s.predictionsPath != "" {
		if err := matcher.AppendPrediction(s.predictionsPath, pred); err != nil {
			log.Printf("[hook] write prediction: %v", err)
		} else {
			s.mu.Lock()
			s.stats.predictionsWritten++
			s.mu.Unlock()
			contextMsg = fmt.Sprintf("[sysbox] step=%d node=%s ttp=%s rule=%s events=%d",
				step, node, pred.TTP, pred.ExtractorRule, len(pred.ExpectedEvents))
		}
	}

	resp := buildAllowResponse(contextMsg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// handlePostToolUse optionally supplements IoC from command output (async).
func (s *Server) handlePostToolUse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// For now: accept and return 200. Future: parse stdout for RCE confirmation.
	w.WriteHeader(http.StatusOK)
}

// sessionRegisterRequest is the body for POST /hooks/session.
type sessionRegisterRequest struct {
	RunID     string `json:"run_id"`
	StateFile string `json:"state_file,omitempty"`
}

// handleSession registers a Langfuse run_id for a Claude session.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Could be a Claude Code SessionStart event (has session_id) or our custom register call.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var req struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
	}
	_ = json.Unmarshal(body, &req)

	if req.SessionID != "" && req.RunID != "" {
		s.mu.Lock()
		s.runIDs[req.SessionID] = req.RunID
		s.mu.Unlock()
		log.Printf("[hook] session %s → run_id %s", req.SessionID, req.RunID)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"status":               "ok",
		"predictions_written":  s.stats.predictionsWritten,
		"requests_handled":     s.stats.requestsHandled,
		"active_sessions":      len(s.stepCounter),
	})
}

// resolveNode identifies the target sysbox node from a Bash command.
//
// Resolution order:
//  1. SSH target IP → lookup in sysbox state by NIC IP
//  2. SSH target hostname → direct node name match
//  3. Last observed node for this session (sticky)
//  4. Fallback: "unknown"
func (s *Server) resolveNode(sessionID, command string) string {
	target := extractSSHTarget(command)
	if target != "" {
		if s.st != nil {
			if node := findNodeByIP(s.st, target); node != "" {
				s.mu.Lock()
				s.lastNode[sessionID] = node
				s.mu.Unlock()
				return node
			}
		}
		// Try direct node name match.
		if s.st != nil {
			for _, r := range s.st.Resources {
				if r.Type == "sysbox_node" && r.Name == target {
					s.mu.Lock()
					s.lastNode[sessionID] = r.Name
					s.mu.Unlock()
					return r.Name
				}
			}
		}
	}

	s.mu.Lock()
	last := s.lastNode[sessionID]
	s.mu.Unlock()
	if last != "" {
		return last
	}
	return "unknown"
}

// SSH patterns: ssh [opts] [user@]<host> [cmd]
var sshHostRe = regexp.MustCompile(`\bssh\b[^'"\n]*?(?:[\w.]+@)?([\d.]+|[\w][\w.-]+)\b`)

func extractSSHTarget(command string) string {
	// Handle: ssh root@10.0.1.2 ..., ssh -p 22 10.0.1.2 ...
	m := sshHostRe.FindStringSubmatch(command)
	if m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func findNodeByIP(st *pkgstate.State, ip string) string {
	for _, r := range st.Resources {
		if r.Type != "sysbox_node" {
			continue
		}
		nics, ok := r.Instance["nics"].([]any)
		if !ok {
			continue
		}
		for _, n := range nics {
			nm, ok := n.(map[string]any)
			if !ok {
				continue
			}
			nicIP, _ := nm["ip"].(string)
			// nicIP is CIDR like "10.0.1.2/24"
			if strings.HasPrefix(nicIP, ip+"/") || nicIP == ip {
				return r.Name
			}
		}
	}
	return ""
}

// buildAllowResponse returns a Claude Code HTTP hook response that
// allows the tool call and optionally injects context for Claude.
func buildAllowResponse(additionalContext string) map[string]any {
	resp := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":    "PreToolUse",
			"permissionDecision": "allow",
		},
	}
	if additionalContext != "" {
		resp["hookSpecificOutput"].(map[string]any)["additionalContext"] = additionalContext
	}
	return resp
}
