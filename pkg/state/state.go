// Package state manages sysbox's persistent state file.
//
// Each sysbox apply writes resource entries to a JSON state file.
// The state is the single source of truth for what's currently deployed.
//
// SchemaVersion is bumped on every breaking format change. v1.0 introduces
// schema v2: typed NodeHandle, ProviderExtra json blob, no v1→v2 migration.
// Loading a v1 file fails with a clear error pointing users to clear runs/.
package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SchemaVersion is the current persistent format version.
//
//	v1 – sysbox v0.x (pre-multi-substrate cleanup)
//	v2 – sysbox v1.0 (typed NodeHandle, ProviderExtra blob)
const SchemaVersion = 2

type State struct {
	mu        sync.RWMutex `json:"-"`
	Version   int          `json:"version"`
	RunID     string       `json:"run_id"`
	Resources []Resource   `json:"resources"`
}

type Resource struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Instance  map[string]any `json:"instance"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
}

// Int returns the value at key as an int. JSON round-trip stores numbers as
// float64, so both int and float64 are accepted. Returns 0 if the key is
// missing or the type doesn't match.
func (r *Resource) Int(key string) int {
	switch v := r.Instance[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// Str returns the value at key as a string. Returns "" if missing or wrong type.
func (r *Resource) Str(key string) string {
	s, _ := r.Instance[key].(string)
	return s
}

// Slice returns the value at key as []any. Returns nil if missing or wrong type.
func (r *Resource) Slice(key string) []any {
	v, _ := r.Instance[key].([]any)
	return v
}

// Map returns the value at key as map[string]any. Returns nil if missing.
func (r *Resource) Map(key string) map[string]any {
	v, _ := r.Instance[key].(map[string]any)
	return v
}

// Float returns the value at key as float64. Returns 0 if missing or wrong type.
func (r *Resource) Float(key string) float64 {
	switch v := r.Instance[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func (s *State) Marshal() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s, "", "  ")
}

func Unmarshal(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	if s.Version != SchemaVersion {
		return nil, &IncompatibleVersionError{Found: s.Version, Expected: SchemaVersion}
	}
	return &s, nil
}

// IncompatibleVersionError is returned by Unmarshal when the on-disk state
// version does not match the binary's SchemaVersion. v1.0 deliberately does
// not auto-migrate v1 state files; users must clear runs/ and re-apply.
type IncompatibleVersionError struct {
	Found    int
	Expected int
}

func (e *IncompatibleVersionError) Error() string {
	return fmt.Sprintf(
		"state schema v%d is incompatible with sysbox binary (expects v%d). "+
			"sysbox v1.0 deliberately dropped v1 state migration; "+
			"please clear runs/ (rm -rf runs/) and re-apply.",
		e.Found, e.Expected,
	)
}

func (s *State) FindResource(typ, name string) *Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.Resources {
		if s.Resources[i].Type == typ && s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}

func (s *State) AddResource(r Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.UpdatedAt = r.CreatedAt
	s.Resources = append(s.Resources, r)
}

func (s *State) RemoveResource(typ, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]Resource, 0, len(s.Resources))
	for _, r := range s.Resources {
		if r.Type == typ && r.Name == name {
			continue
		}
		filtered = append(filtered, r)
	}
	s.Resources = filtered
}
