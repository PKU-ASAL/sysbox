package persistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type fileGuestRecords struct{ root string }

func NewFilesystemGuestRepository(runsDir string) *GuestRepository {
	return &GuestRepository{records: &fileGuestRecords{root: runsDir}}
}
func (b *fileGuestRecords) directory(kind guestKind) (string, error) {
	if b.root == "" {
		return "", ErrInvalidRecord
	}
	switch kind {
	case executionAuthorityKind:
		return filepath.Join(b.root, "_execution-authority"), nil
	case guestExecutionKind:
		return filepath.Join(b.root, "_guest-executions"), nil
	case guestFileKind:
		return filepath.Join(b.root, "_guest-file-operations"), nil
	case nodeOperationKind:
		legacy, err := filepath.Glob(filepath.Join(b.root, "*", "node-ops", "*.json"))
		if err != nil {
			return "", err
		}
		if len(legacy) > 0 {
			return "", ErrUnsupportedVersion
		}
		return filepath.Join(b.root, "_node-operations"), nil
	default:
		return "", ErrInvalidRecord
	}
}
func (b *fileGuestRecords) get(ctx context.Context, kind guestKind, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := b.directory(kind)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return raw, err
}
func (b *fileGuestRecords) list(ctx context.Context, kind guestKind) ([]guestRawRecord, error) {
	dir, err := b.directory(kind)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]guestRawRecord, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !topologyName.MatchString(id) {
			return nil, ErrCorruptRecord
		}
		raw, err := b.get(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		out = append(out, guestRawRecord{id: id, data: raw})
	}
	return out, nil
}
func (b *fileGuestRecords) update(ctx context.Context, kind guestKind, id string, fn func([]byte) ([]byte, error)) error {
	if isOperationRecord(kind) {
		return b.updateOperation(ctx, kind, id, fn)
	}
	return b.updateRecord(ctx, kind, id, fn)
}

func (b *fileGuestRecords) updateRecord(ctx context.Context, kind guestKind, id string, fn func([]byte) ([]byte, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := b.directory(kind)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, id+".lock"))
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	raw, err := b.get(ctx, kind, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if errors.Is(err, ErrNotFound) {
		raw = nil
	}
	next, err := fn(raw)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	return writePrivateGuestRecord(ctx, filepath.Join(dir, id+".json"), next)
}
func writePrivateGuestRecord(ctx context.Context, path string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	// Persist the record and newly created directory entries, including parents
	// left behind by an earlier interrupted attempt. Lock inodes are never unlinked.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for {
		d, err := os.Open(abs)
		if err != nil {
			return err
		}
		syncErr := d.Sync()
		closeErr := d.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}
