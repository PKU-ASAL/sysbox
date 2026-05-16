// Package state manages sysbox's persistent state file.
//
// Each sysbox apply writes resource entries to a JSON state file.
// The state is the single source of truth for what's currently deployed.
package state

import (
	"encoding/json"
	"fmt"
	"sync"
)

type State struct {
	mu        sync.RWMutex `json:"-"`
	Version   int          `json:"version"`
	RunID     string       `json:"run_id"`
	Resources []Resource   `json:"resources"`
}

type Resource struct {
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Provider string         `json:"provider"`
	Instance map[string]any `json:"instance"`
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
	return &s, nil
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
