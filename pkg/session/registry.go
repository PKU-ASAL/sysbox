package session

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Expectation is a pre-registered session declaration written by
// `sysbox session register` before the attacker connects.
//
// The sysbox-sshd-hook resolves this table when a new SSH connection arrives;
// if the (node, source_ip) pair matches an unexpired entry, the hook uses
// the pre-declared session_id instead of generating a random UUID.
// This lets the experiment layer correlate sysbox session_id with an external
// trace id (Langfuse run ID, OTEL trace, etc.).
type Expectation struct {
	NodeID    string    `json:"node_id"`
	SourceIP  string    `json:"source_ip,omitempty"` // "" = match any source
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Registry persists Expectations as a JSON file under runs/<runID>/.
// It is intentionally simple: no daemon, no socket.
type Registry struct {
	path string
	mu   sync.Mutex
}

// NewRegistry returns a Registry backed by the given file path.
func NewRegistry(path string) *Registry { return &Registry{path: path} }

// Register appends an Expectation. Thread-safe.
func (r *Registry) Register(exp Expectation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, _ := r.loadLocked()
	entries = append(entries, exp)
	return r.saveLocked(entries)
}

// Resolve returns the pre-declared session_id for the given (node, sourceIP)
// pair if an unexpired Expectation exists, otherwise "".
// Consuming the expectation (removing after resolve) is intentional: each
// pre-declaration is matched at most once.
func (r *Registry) Resolve(nodeID, sourceIP string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.loadLocked()
	if err != nil || len(entries) == 0 {
		return ""
	}

	now := time.Now()
	var remaining []Expectation
	found := ""

	for _, e := range entries {
		if e.NodeID == nodeID &&
			(e.SourceIP == "" || e.SourceIP == sourceIP) &&
			now.Before(e.ExpiresAt) &&
			found == "" {
			found = e.SessionID
			continue // consume: don't keep in remaining
		}
		remaining = append(remaining, e)
	}

	_ = r.saveLocked(remaining)
	return found
}

// List returns all unexpired expectations (for CLI display).
func (r *Registry) List() []Expectation {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, _ := r.loadLocked()
	now := time.Now()
	var out []Expectation
	for _, e := range entries {
		if now.Before(e.ExpiresAt) {
			out = append(out, e)
		}
	}
	return out
}

func (r *Registry) loadLocked() ([]Expectation, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Expectation
	return out, json.Unmarshal(data, &out)
}

func (r *Registry) saveLocked(entries []Expectation) error {
	if entries == nil {
		entries = []Expectation{}
	}
	data, _ := json.MarshalIndent(entries, "", "  ")
	return os.WriteFile(r.path, data, 0o644)
}
