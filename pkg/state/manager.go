package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type Manager struct {
	path string
}

func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// Load reads the state file. Missing file returns empty state, not error.
func (m *Manager) Load() (*State, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &State{Version: 1}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	return Unmarshal(data)
}

// Save atomically writes state to disk: write temp file, then rename.
// Acquires a file lock to prevent concurrent writers.
func (m *Manager) Save(s *State) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	lock := flock.New(m.path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire state lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("state is locked by another process")
	}
	defer lock.Unlock()

	data, err := s.Marshal()
	if err != nil {
		return err
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	return os.Rename(tmp, m.path)
}

// Path returns the state file path managed by this Manager.
func (m *Manager) Path() string { return m.path }
