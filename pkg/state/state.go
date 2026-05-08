// Package state manages sysbox's persistent state file.
//
// Each sysbox apply writes resource entries to a JSON state file.
// The state is the single source of truth for what's currently deployed.
package state

import (
	"encoding/json"
	"fmt"
)

type State struct {
	Version   int        `json:"version"`
	RunID     string     `json:"run_id"`
	Resources []Resource `json:"resources"`
}

type Resource struct {
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Provider string         `json:"provider"`
	Instance map[string]any `json:"instance"`
}

func (s *State) Marshal() ([]byte, error) {
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
	for i := range s.Resources {
		if s.Resources[i].Type == typ && s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}

func (s *State) AddResource(r Resource) {
	s.Resources = append(s.Resources, r)
}

func (s *State) RemoveResource(typ, name string) {
	out := s.Resources[:0]
	for _, r := range s.Resources {
		if r.Type == typ && r.Name == name {
			continue
		}
		out = append(out, r)
	}
	s.Resources = out
}
