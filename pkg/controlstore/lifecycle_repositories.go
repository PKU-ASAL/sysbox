package controlstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

type LifecycleRepositories struct {
	RepositoryMu sync.Mutex
	Repositories map[string]*persistence.Repository
}

func (s *LifecycleRepositories) Open(runsDir, stateBackend, topology string) (*persistence.Repository, error) {
	if err := ValidatePathSegment(topology, "topology"); err != nil {
		return nil, err
	}
	path := filepath.Join(runsDir, topology, "state.json")
	raw, path, err := persistence.ResolveLocation(stateBackend, path, topology)
	if err != nil {
		return nil, err
	}
	// A cached pool must not hide newly discovered legacy state corruption.
	if raw == "" {
		if _, statErr := os.Stat(path); statErr == nil {
			return nil, fmt.Errorf("%w: standalone state requires explicit rebuild", persistence.ErrUnsupportedVersion)
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}
	key := raw
	if key == "" {
		key = path
	}
	s.RepositoryMu.Lock()
	defer s.RepositoryMu.Unlock()
	if repo := s.Repositories[key]; repo != nil {
		return repo, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return nil, err
	}
	if s.Repositories == nil {
		s.Repositories = map[string]*persistence.Repository{}
	}
	s.Repositories[key] = repo
	return repo, nil
}

func (s *LifecycleRepositories) Close() error {
	s.RepositoryMu.Lock()
	defer s.RepositoryMu.Unlock()
	var failures []error
	for _, repo := range s.Repositories {
		failures = append(failures, repo.Close())
	}
	s.Repositories = nil
	return errors.Join(failures...)
}
