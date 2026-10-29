package persistence

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ConfiguredTopology resolves an explicit backend selector without changing
// storage. CLI uses its state-path topology only as the fallback.
func ConfiguredTopology(backendURL, fallback string) (string, error) {
	if !strings.Contains(backendURL, "://") {
		return fallback, nil
	}
	u, err := url.Parse(backendURL)
	if err != nil {
		return "", err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", err
	}
	if names, exists := q["topology"]; exists {
		if len(names) != 1 || !topologyName.MatchString(names[0]) {
			return "", fmt.Errorf("backend requires exactly one valid topology selector")
		}
		return names[0], nil
	}
	return fallback, nil
}

// ResolveLocation binds a caller's topology before opening or reusing a pool.
// A fixed selector may not redirect an API request to another topology.
func ResolveLocation(backendURL, statePath, topology string) (string, string, error) {
	if !topologyName.MatchString(topology) {
		return "", "", fmt.Errorf("invalid topology %q", topology)
	}
	backendURL = strings.ReplaceAll(backendURL, "{topology}", topology)
	statePath = strings.ReplaceAll(statePath, "{topology}", topology)
	selected, err := ConfiguredTopology(backendURL, topology)
	if err != nil {
		return "", "", err
	}
	if selected != topology {
		return "", "", fmt.Errorf("backend topology %q does not match requested topology %q", selected, topology)
	}
	return backendURL, statePath, nil
}

// OpenCatalog enumerates a shared database without inventing a topology. A
// templated query selector is removed; a fixed selector retains its scope.
// A templated storage path is not enumerable without a separate catalog.
func OpenCatalog(ctx context.Context, backendURL string) (*Repository, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}
	if names := q["topology"]; len(names) == 1 && names[0] == "{topology}" {
		q.Del("topology")
	}
	u.RawQuery = q.Encode()
	if strings.Contains(u.Path, "{topology}") || strings.Contains(u.Host, "{topology}") {
		return nil, fmt.Errorf("topology-templated storage paths require an explicit catalog; use a shared database for inventory")
	}
	return OpenConfigured(ctx, u.String(), "")
}

// SQLitePath is shared by lifecycle and API metadata adapters. Selectors and
// percent-encoding must never accidentally become a different database file.
func SQLitePath(backendURL string) (string, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "sqlite" {
		return "", fmt.Errorf("expected SQLite backend URL")
	}
	path := u.Path
	if path == "" {
		path = u.Host
	}
	if path == "" || path == ":memory:" || strings.Contains(path, "{topology}") {
		return "", fmt.Errorf("SQLite repository requires a resolved durable filesystem path")
	}
	return path, nil
}

// OpenConfigured uses the same topology aggregate for CLI, API and Agent.
// Existing standalone state is rejected; it must never be mistaken for empty
// state when a new engine selects a different storage format.
func OpenConfigured(ctx context.Context, backendURL, statePath string) (*Repository, error) {
	return openConfigured(ctx, backendURL, statePath, true)
}

// OpenCatalogSource opens an existing catalog without schema bootstrap. Read
// only inventory must not create a missing database or initialize its tables.
func OpenCatalogSource(ctx context.Context, backendURL, statePath string) (*Repository, error) {
	return openConfigured(ctx, backendURL, statePath, false)
}

func openConfigured(ctx context.Context, backendURL, statePath string, initialize bool) (*Repository, error) {
	if backendURL == "" {
		backendURL = statePath
	}
	if strings.Contains(backendURL, "{topology}") {
		return nil, fmt.Errorf("resolve topology placeholder before opening repository")
	}
	if !strings.Contains(backendURL, "://") {
		return openLocalState(backendURL)
	}
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, err
	}
	selected, err := ConfiguredTopology(backendURL, "")
	if err != nil {
		return nil, err
	}
	var repo *Repository
	switch u.Scheme {
	case "file", "local":
		path := u.Path
		if path == "" {
			path = u.Host
		}
		repo, err = openLocalState(path)
	case "sqlite":
		path, pathErr := SQLitePath(backendURL)
		if pathErr != nil {
			return nil, pathErr
		}
		repo, err = openSQLite(path, initialize)
	case "postgres", "postgresql":
		q := u.Query()
		q.Del("topology")
		u.RawQuery = q.Encode()
		repo, err = openPostgres(ctx, u.String(), initialize)
	default:
		return nil, fmt.Errorf("backend %q does not support atomic lifecycle transactions", u.Scheme)
	}
	if err != nil {
		return nil, err
	}
	repo.boundTopology = selected
	return repo, nil
}
func openLocalState(path string) (*Repository, error) {
	if path == "" {
		return nil, fmt.Errorf("state path is required")
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: standalone state %s requires explicit rebuild", ErrUnsupportedVersion, path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return OpenFilesystem(path + ".lifecycle")
}
