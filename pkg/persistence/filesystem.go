package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"time"
)

type fileBackend struct{ root string }

func OpenFilesystem(root string) (*Repository, error) {
	if root == "" {
		return nil, fmt.Errorf("empty repository directory")
	}
	return &Repository{backend: &fileBackend{root: root}}, nil
}
func (*fileBackend) close() error { return nil }
func (*fileBackend) name() string { return "filesystem-aggregate" }
func (b *fileBackend) keys(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(b.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.Ext(entry.Name()) == ".json" {
			keys = append(keys, entry.Name()[:len(entry.Name())-5])
		}
	}
	return keys, nil
}
func (b *fileBackend) update(ctx context.Context, key string, fn func(*aggregate) error) error {
	return b.updateDecoded(ctx, key, decodeAggregate, fn)
}

func (b *fileBackend) updateDecoded(ctx context.Context, key string, decode func([]byte) (*aggregate, error), fn func(*aggregate) error) error {
	if err := os.MkdirAll(b.root, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(b.root, key+".lock"))
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	path := filepath.Join(b.root, key+".json")
	raw, err := os.ReadFile(path)
	var a *aggregate
	if os.IsNotExist(err) {
		a = newAggregate()
	} else if err != nil {
		return err
	} else {
		a, err = decode(raw)
		if err != nil {
			return err
		}
	}
	if err := fn(a); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	next, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if string(next) == string(raw) {
		return nil
	}
	tmp, err := os.CreateTemp(b.root, ".aggregate-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(next); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(b.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
