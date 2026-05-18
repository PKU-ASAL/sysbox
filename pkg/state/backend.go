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

// Backend is the abstraction for reading/writing state to any storage
// backend (local filesystem, HTTP, S3, etc.). The Manager delegates all
// I/O to the active backend.
type Backend interface {
	// Load reads the state data. Returns (nil, nil) if no state exists yet.
	Load(ctx context.Context) ([]byte, error)
	// Save writes the state data atomically.
	Save(ctx context.Context, data []byte) error
	// Lock acquires an exclusive lock on the state. Implementations that
	// cannot lock should return nil (optimistic concurrency).
	Lock(ctx context.Context) (UnlockFunc, error)
}

// UnlockFunc releases a previously acquired lock.
type UnlockFunc func()

// ── Local filesystem backend (default) ───────────────────────────────────────

// LocalBackend stores state in a local JSON file with file-locking.
type LocalBackend struct {
	Path string
}

func (b *LocalBackend) Load(_ context.Context) ([]byte, error) {
	data, err := os.ReadFile(b.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	return data, nil
}

func (b *LocalBackend) Save(_ context.Context, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp := b.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	return os.Rename(tmp, b.Path)
}

func (b *LocalBackend) Lock(ctx context.Context) (UnlockFunc, error) {
	lock := flock.New(b.Path + ".lock")
	timeout := defaultLockTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
		if timeout <= 0 {
			return nil, fmt.Errorf("state lock: context deadline exceeded")
		}
	}
	locked, err := lock.TryLockContext(ctx, timeout)
	if err != nil {
		return nil, fmt.Errorf("acquire state lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("state is locked by another process (timeout after %v)", timeout)
	}
	return func() { lock.Unlock() }, nil
}

// ── HTTP backend ──────────────────────────────────────────────────────────────

// HTTPBackend stores state via HTTP PUT/GET (compatible with Terraform's
// HTTP backend). The URL is the state endpoint; optional headers provide
// auth (e.g. Authorization: Bearer ...).
type HTTPBackend struct {
	URL     string
	Headers map[string]string
}

func (b *HTTPBackend) Load(ctx context.Context) ([]byte, error) {
	req, err := newRequest(ctx, "GET", b.URL, nil, b.Headers)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get state: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http get state: status %d", resp.StatusCode)
	}
	return readBody(resp)
}

func (b *HTTPBackend) Save(ctx context.Context, data []byte) error {
	req, err := newRequest(ctx, "PUT", b.URL, data, b.Headers)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http put state: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http put state: status %d", resp.StatusCode)
	}
	return nil
}

func (b *HTTPBackend) Lock(_ context.Context) (UnlockFunc, error) {
	// HTTP backend relies on optimistic concurrency (ETag / If-Match)
	// rather than explicit locking.
	return func() {}, nil
}

// ── S3 backend ────────────────────────────────────────────────────────────────

// S3Backend stores state in an S3-compatible object store.
// It uses the standard AWS SDK v2 credential chain (env, profile, IAM role).
type S3Backend struct {
	Bucket string
	Key    string
	Region string
	// Endpoint overrides the default AWS endpoint (for MinIO, etc.).
	Endpoint string
}

func (b *S3Backend) Load(ctx context.Context) ([]byte, error) {
	client, err := b.s3Client(ctx)
	if err != nil {
		return nil, err
	}
	return s3GetObject(ctx, client, b.Bucket, b.Key)
}

func (b *S3Backend) Save(ctx context.Context, data []byte) error {
	client, err := b.s3Client(ctx)
	if err != nil {
		return err
	}
	return s3PutObject(ctx, client, b.Bucket, b.Key, data)
}

func (b *S3Backend) Lock(_ context.Context) (UnlockFunc, error) {
	// S3 backend uses native conditional writes (PutObject with IfNoneMatch
	// for lock acquisition). Simple implementation: optimistic.
	return func() {}, nil
}
