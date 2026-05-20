package state

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ParseBackendURL constructs a Backend from a URL-like string:
//
//   - "local:///path/to/state.json" or plain path → LocalBackend
//   - "https://host/path/state.json"            → HTTPBackend
//   - "s3://bucket/key"                         → S3Backend
//   - "sqlite:///path/to/sysbox.db?topology=x"  → SQLiteBackend
//   - "postgres://user:pass@host/db?topology=x" → PostgresBackend
//
// For the default (local) case, a plain file path is accepted and
// wrapped in LocalBackend automatically.
func ParseBackendURL(raw string) (Backend, error) {
	// Plain path (no scheme) → local backend.
	if !strings.Contains(raw, "://") {
		return &LocalBackend{Path: raw}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse backend URL: %w", err)
	}

	switch u.Scheme {
	case "file", "local":
		return &LocalBackend{Path: u.Path}, nil
	case "http", "https":
		headers := map[string]string{}
		// Inject Authorization header from environment variable.
		if token := os.Getenv("SYSBOX_STATE_AUTH"); token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		return &HTTPBackend{
			URL:     raw,
			Headers: headers,
		}, nil
	case "s3":
		bucket := u.Host
		key := strings.TrimPrefix(u.Path, "/")
		if bucket == "" || key == "" {
			return nil, fmt.Errorf("s3 backend URL must be s3://bucket/key, got %q", raw)
		}
		return &S3Backend{
			Bucket: bucket,
			Key:    key,
			Region: envOrDefault("AWS_REGION", envOrDefault("AWS_DEFAULT_REGION", "us-east-1")),
		}, nil
	case "sqlite":
		path := u.Path
		if path == "" {
			path = u.Host
		}
		if path == "" {
			return nil, fmt.Errorf("sqlite backend URL must include a database path")
		}
		return &SQLiteBackend{Path: path, Topology: u.Query().Get("topology")}, nil
	case "postgres", "postgresql":
		return &PostgresBackend{DSN: raw, Topology: u.Query().Get("topology")}, nil
	default:
		return nil, fmt.Errorf("unsupported state backend scheme %q (use local, http, https, s3, sqlite, or postgres)", u.Scheme)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
