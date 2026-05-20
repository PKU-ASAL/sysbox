package state

import (
	"context"
)

// Manager reads and writes state through a Backend. By default it uses
// LocalBackend (file on disk with flock). The backend can be swapped for
// HTTP or S3 by calling SetBackend before Load/Save.
type Manager struct {
	backend Backend
}

func NewManager(path string) *Manager {
	return &Manager{
		backend: &LocalBackend{Path: path},
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

func (m *Manager) Metadata(ctx context.Context) (Metadata, error) {
	if b, ok := m.backend.(MetadataBackend); ok {
		return b.Metadata(ctx)
	}
	return Metadata{Backend: "unknown", Version: SchemaVersion}, nil
}

func (m *Manager) Snapshot(ctx context.Context, reason string) (*Snapshot, error) {
	if b, ok := m.backend.(SnapshotBackend); ok {
		return b.Snapshot(ctx, reason)
	}
	return nil, nil
}

func (m *Manager) Delete(ctx context.Context) error {
	if b, ok := m.backend.(DeleteBackend); ok {
		return b.Delete(ctx)
	}
	return nil
}

func (m *Manager) LockInfo(ctx context.Context) (LockInfo, error) {
	if b, ok := m.backend.(LockInfoBackend); ok {
		return b.LockInfo(ctx)
	}
	return LockInfo{}, nil
}

func (m *Manager) ForceUnlock(ctx context.Context) error {
	if b, ok := m.backend.(LockInfoBackend); ok {
		return b.ForceUnlock(ctx)
	}
	return nil
}

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

	unlock, err := m.lock(ctx, LockOptions{})
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

func (m *Manager) lock(ctx context.Context, opts LockOptions) (UnlockFunc, error) {
	if b, ok := m.backend.(LeaseBackend); ok {
		_, unlock, err := b.LockWithOptions(ctx, opts)
		return unlock, err
	}
	return m.backend.Lock(ctx)
}
