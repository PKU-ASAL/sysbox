package state

import (
	"context"
)

// Manager reads and writes state through a Backend. By default it uses
// LocalBackend (file on disk with flock). The backend can be swapped for
// HTTP or S3 by calling SetBackend before Load/Save.
type Manager struct {
	backend Backend
	path    string // kept for backwards compat with Path()
}

func NewManager(path string) *Manager {
	return &Manager{
		backend: &LocalBackend{Path: path},
		path:    path,
	}
}

// NewManagerWithBackend creates a Manager with a custom backend.
func NewManagerWithBackend(b Backend) *Manager {
	return &Manager{backend: b}
}

// SetBackend replaces the active backend. Must be called before Load/Save.
func (m *Manager) SetBackend(b Backend) { m.backend = b }

// Backend returns the active backend.
func (m *Manager) Backend() Backend { return m.backend }

// Load reads the state from the active backend.
// Missing state returns an empty State, not an error.
func (m *Manager) Load() (*State, error) {
	return m.LoadWithContext(context.Background())
}

// LoadWithContext is like Load but respects the given context.
func (m *Manager) LoadWithContext(ctx context.Context) (*State, error) {
	data, err := m.backend.Load(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return &State{Version: SchemaVersion}, nil
	}
	return Unmarshal(data)
}

// Save writes state through the active backend.
func (m *Manager) Save(s *State) error {
	return m.SaveWithContext(context.Background(), s)
}

// SaveWithContext is like Save but respects the given context.
func (m *Manager) SaveWithContext(ctx context.Context, s *State) error {
	s.Version = SchemaVersion

	unlock, err := m.backend.Lock(ctx)
	if err != nil {
		return err
	}
	if unlock != nil {
		defer unlock()
	}

	data, err := s.Marshal()
	if err != nil {
		return err
	}
	return m.backend.Save(ctx, data)
}

// Path returns the state file path (local backend only).
func (m *Manager) Path() string { return m.path }
