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

type Metadata struct {
	Backend     string    `json:"backend"`
	Location    string    `json:"location"`
	Version     int       `json:"version,omitempty"`
	Serial      int64     `json:"serial,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	LockOwner   string    `json:"lock_owner,omitempty"`
	LockExpires time.Time `json:"lock_expires,omitempty"`
}

type LoadedState struct {
	Data      []byte
	Metadata  Metadata
	Exists    bool
	Serial    int64
	UpdatedAt time.Time
}

type SaveOptions struct {
	ExpectedSerial int64
	RequireCAS     bool
}

type TopologyMetadata struct {
	Name          string    `json:"name"`
	Backend       string    `json:"backend"`
	HasState      bool      `json:"has_state"`
	ResourceCount int       `json:"resource_count,omitempty"`
	Serial        int64     `json:"serial,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type ConflictError struct {
	Expected int64
	Actual   int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("state serial conflict: expected %d, got %d", e.Expected, e.Actual)
}

type LockOptions struct {
	Owner string
	TTL   time.Duration
}

type Lease struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Snapshot struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason,omitempty"`
	Size      int64     `json:"size"`
}

type MetadataBackend interface {
	Metadata(ctx context.Context) (Metadata, error)
}

type VersionedBackend interface {
	LoadVersioned(ctx context.Context) (*LoadedState, error)
	SaveVersioned(ctx context.Context, data []byte, opts SaveOptions) error
}

type TopologyLister interface {
	ListTopologies(ctx context.Context) ([]TopologyMetadata, error)
}

type LeaseBackend interface {
	LockWithOptions(ctx context.Context, opts LockOptions) (*Lease, UnlockFunc, error)
}

type SnapshotBackend interface {
	Snapshot(ctx context.Context, reason string) (*Snapshot, error)
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	RestoreSnapshot(ctx context.Context, id string) error
}

type DeleteBackend interface {
	Delete(ctx context.Context) error
}

type LockInfo struct {
	Locked    bool      `json:"locked"`
	Owner     string    `json:"owner,omitempty"`
	LeaseID   string    `json:"lease_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type LockInfoBackend interface {
	LockInfo(ctx context.Context) (LockInfo, error)
	ForceUnlock(ctx context.Context) error
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
	if err := b.bumpSerial(); err != nil {
		return err
	}
	tmp := b.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	return os.Rename(tmp, b.Path)
}

func (b *LocalBackend) Lock(ctx context.Context) (UnlockFunc, error) {
	if err := os.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
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

func (b *LocalBackend) LockWithOptions(ctx context.Context, opts LockOptions) (*Lease, UnlockFunc, error) {
	unlock, err := b.Lock(ctx)
	if err != nil {
		return nil, nil, err
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultLockTimeout
	}
	lease := &Lease{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Owner:     opts.Owner,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	return lease, unlock, nil
}

func (b *LocalBackend) Metadata(_ context.Context) (Metadata, error) {
	meta := Metadata{Backend: "local", Location: b.Path, Version: SchemaVersion}
	if st, err := os.Stat(b.Path); err == nil {
		meta.UpdatedAt = st.ModTime().UTC()
	}
	serial, _ := readSerial(b.serialPath())
	meta.Serial = serial
	return meta, nil
}

func (b *LocalBackend) Snapshot(_ context.Context, reason string) (*Snapshot, error) {
	data, err := b.Load(context.Background())
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	if err := os.MkdirAll(b.snapshotDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z")
	path := filepath.Join(b.snapshotDir(), id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("write snapshot: %w", err)
	}
	if reason != "" {
		_ = os.WriteFile(path+".reason", []byte(reason), 0o644)
	}
	return &Snapshot{ID: id, CreatedAt: now, Reason: reason, Size: int64(len(data))}, nil
}

func (b *LocalBackend) ListSnapshots(_ context.Context) ([]Snapshot, error) {
	entries, err := os.ReadDir(b.snapshotDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		reason, _ := os.ReadFile(filepath.Join(b.snapshotDir(), entry.Name()+".reason"))
		out = append(out, Snapshot{ID: id, CreatedAt: info.ModTime().UTC(), Reason: string(reason), Size: info.Size()})
	}
	return out, nil
}

func (b *LocalBackend) RestoreSnapshot(_ context.Context, id string) error {
	if id == "" || filepath.Base(id) != id {
		return fmt.Errorf("invalid snapshot id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(b.snapshotDir(), id+".json"))
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	return b.Save(context.Background(), data)
}

func (b *LocalBackend) snapshotDir() string {
	return filepath.Join(filepath.Dir(b.Path), ".snapshots")
}

func (b *LocalBackend) serialPath() string {
	return b.Path + ".serial"
}

func (b *LocalBackend) bumpSerial() error {
	serial, _ := readSerial(b.serialPath())
	return os.WriteFile(b.serialPath(), []byte(fmt.Sprintf("%d\n", serial+1)), 0o644)
}

func readSerial(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var serial int64
	_, err = fmt.Sscanf(string(data), "%d", &serial)
	return serial, err
}

// ── HTTP backend ──────────────────────────────────────────────────────────────

// HTTPBackend stores state via HTTP PUT/GET (compatible with Terraform's
// HTTP backend). The URL is the state endpoint; optional headers provide
// auth (e.g. Authorization: Bearer ...).
//
// Limitations: HTTPBackend implements only the base Backend interface — no
// versioned CAS saves, snapshots, deletes, or locking. Versioned features
// silently fall back to plain Save/Load. Use the Postgres backend when those
// guarantees matter.
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
	// The HTTP backend implements no locking: Save is a plain PUT with no
	// conditional headers, so concurrent writers can overwrite each other.
	// Use the Postgres backend when multiple writers share a topology.
	return func() {}, nil
}

func (b *HTTPBackend) Metadata(_ context.Context) (Metadata, error) {
	return Metadata{Backend: "http", Location: b.URL, Version: SchemaVersion}, nil
}

// ── S3 backend ────────────────────────────────────────────────────────────────

// S3Backend stores state in an S3-compatible object store by shelling out to
// the `aws` CLI (no SDK dependency); credentials come from the CLI's standard
// chain (env, profile, IAM role).
//
// Limitations: like HTTPBackend, it implements only the base Backend
// interface — no versioned CAS saves, snapshots, deletes, or locking.
// Concurrent writers can overwrite each other; use Postgres for multi-writer
// setups.
type S3Backend struct {
	Bucket string
	Key    string
	Region string
	// Endpoint overrides the default AWS endpoint (for MinIO, etc.).
	Endpoint string
}

func (b *S3Backend) Lock(_ context.Context) (UnlockFunc, error) {
	// No locking: Save is a plain `aws s3 cp` upload with no conditional
	// headers, so concurrent writers can overwrite each other.
	return func() {}, nil
}

func (b *S3Backend) Metadata(_ context.Context) (Metadata, error) {
	return Metadata{Backend: "s3", Location: fmt.Sprintf("s3://%s/%s", b.Bucket, b.Key), Version: SchemaVersion}, nil
}

type PostgresBackend struct {
	DSN      string
	Topology string
}
