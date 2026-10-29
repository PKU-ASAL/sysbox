package api

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

type lifecycleRepositories struct {
	repositoryMu sync.Mutex
	repositories map[string]*persistence.Repository
}

func (s *lifecycleRepositories) open(runsDir, stateBackend, topology string) (*persistence.Repository, error) {
	if err := validatePathSegment(topology, "topology"); err != nil {
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
	s.repositoryMu.Lock()
	defer s.repositoryMu.Unlock()
	if repo := s.repositories[key]; repo != nil {
		return repo, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := persistence.OpenConfigured(ctx, raw, path)
	if err != nil {
		return nil, err
	}
	if s.repositories == nil {
		s.repositories = map[string]*persistence.Repository{}
	}
	s.repositories[key] = repo
	return repo, nil
}

func (s *lifecycleRepositories) close() error {
	s.repositoryMu.Lock()
	defer s.repositoryMu.Unlock()
	var failures []error
	for _, repo := range s.repositories {
		failures = append(failures, repo.Close())
	}
	s.repositories = nil
	return errors.Join(failures...)
}
