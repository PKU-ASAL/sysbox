package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const defaultLockTimeout = 10 * time.Second

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
// Acquires a file lock with a timeout so concurrent apply/sensor don't
// immediately fail when briefly contending.
func (m *Manager) Save(s *State) error {
	return m.SaveWithContext(context.Background(), s)
}

// SaveWithContext is like Save but respects the given context for lock
// acquisition. Defaults to a 10-second timeout if the context has no
// deadline.
func (m *Manager) SaveWithContext(ctx context.Context, s *State) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	lock := flock.New(m.path + ".lock")
	timeout := defaultLockTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
		if timeout <= 0 {
			return fmt.Errorf("state lock: context deadline exceeded")
		}
	}

	locked, err := lock.TryLockContext(ctx, timeout)
	if err != nil {
		return fmt.Errorf("acquire state lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("state is locked by another process (timeout after %v)", timeout)
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
