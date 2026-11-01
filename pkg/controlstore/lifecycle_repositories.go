package controlstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/persistence"
)

type LifecycleRepositories struct {
	RepositoryMu         sync.Mutex
	Repositories         map[string]*persistence.Repository
	databaseRepositories map[string]*persistence.Repository
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
	// A topology selector is an authorization boundary, not a distinct
	// database. Validate it above, then share only the physical connection pool.
	if u, parseErr := url.Parse(raw); parseErr == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql" || u.Scheme == "sqlite") {
		query := u.Query()
		query.Del("topology")
		u.RawQuery = query.Encode()
		if u.Scheme == "postgresql" {
			u.Scheme = "postgres"
		}
		databaseKey := u.String()
		owner := s.databaseRepositories[databaseKey]
		if owner == nil {
			owner, err = persistence.OpenConfigured(ctx, databaseKey, path)
			if err != nil {
				return nil, err
			}
			if s.databaseRepositories == nil {
				s.databaseRepositories = map[string]*persistence.Repository{}
			}
			s.databaseRepositories[databaseKey] = owner
		}
		key = databaseKey + "\x00" + topology
		if existing := s.Repositories[key]; existing != nil {
			return existing, nil
		}
		repo, err := owner.BindTopology(topology)
		if err != nil {
			return nil, err
		}
		if s.Repositories == nil {
			s.Repositories = map[string]*persistence.Repository{}
		}
		s.Repositories[key] = repo
		return repo, nil
	}
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
	for _, repo := range s.databaseRepositories {
		failures = append(failures, repo.Close())
	}
	s.databaseRepositories = nil
	return errors.Join(failures...)
}
