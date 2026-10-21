package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/revisiongate"
)

func (s *localAPIStore) SaveGlobalRevisionAtomic(ctx context.Context, rev controlplane.GlobalRevision) error {
	// Global registry publication uses its explicit catalog, even if discovery metadata is missing.
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := revisiongate.Catalog(s.runsDir)
	if err != nil {
		return err
	}

	if !globalRevisionPattern.MatchString(rev.Revision) {
		return persistence.ErrInvalidRecord
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rev, "", "  ")
	if err != nil {
		return err
	}
	release, err := revisiongate.GuardCatalog(ctx, root, raw)
	if err != nil {
		return err
	}
	defer release()
	if _, bindingErr := os.Lstat(filepath.Join(s.runsDir, ".sysbox-revision-registry.json")); bindingErr == nil {
		roots, err := revisiongate.Consumers(root)
		if err != nil {
			return err
		}
		found := false
		for _, consumer := range roots {
			if consumer == s.runsDir {
				found = true
			}
		}
		if !found {
			return persistence.ErrConflict
		}
	} else if !os.IsNotExist(bindingErr) {
		return bindingErr
	}
	return publishLocalObject(filepath.Join(root, rev.Revision+".json"), raw)
}
func (s *localAPIStore) DeleteGlobalRevisionAtomic(ctx context.Context, id string, olderThan time.Time) error {
	if !globalRevisionPattern.MatchString(id) || olderThan.IsZero() || olderThan.After(time.Now()) {
		return persistence.ErrInvalidRecord
	}
	root, err := revisiongate.Catalog(s.runsDir)
	if err != nil {
		return err
	}
	unlock, err := revisiongate.Lock(ctx, root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.validateLocalRevisionRetirement(ctx, root, id, olderThan); err != nil {
		return err
	}
	return compactLocalRevisionRetirement(ctx, root, id)
}

func (s *localAPIStore) validateLocalRevisionRetirement(ctx context.Context, root, id string, olderThan time.Time) error {
	if err := revisiongate.ValidateRemoteConsumers(root, id, time.Now()); err != nil {
		return err
	}
	rev, err := s.GetGlobalRevision(ctx, id)
	if err != nil {
		return err
	}
	if controlplane.ComputeRevisionDigest(rev.Files) != id || rev.CreatedAt.IsZero() {
		return persistence.ErrCorruptRecord
	}
	if !rev.CreatedAt.Before(olderThan) {
		return persistence.ErrConflict
	}
	target := filepath.Join(root, id+".json")
	count, total := 0, int64(0)
	scan := func(scanRoot string) error {
		return filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: symlink in reference catalog", persistence.ErrConflict)
			}
			if entry.IsDir() {
				if filepath.Base(path) == id {
					return fmt.Errorf("%w: retained materialized revision", persistence.ErrConflict)
				}
				return nil
			}
			if path == target || (filepath.Dir(path) == root && revisiongate.IsRetirementMetadata(filepath.Base(path))) || path == filepath.Join(root, "consumers.json") || filepath.Base(path) == ".sysbox-revision-registry.json" || strings.HasSuffix(path, ".lock") {
				return nil
			}
			if strings.HasSuffix(path, ".retired") {
				return nil
			}
			if !(strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".checkpoint.json.returned")) {
				return nil
			}
			if filepath.Base(path) == "state.json" {
				return persistence.ErrUnsupportedVersion
			}
			count++
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return persistence.ErrCorruptRecord
			}
			total += info.Size()
			if count > 10000 || total > 64<<20 {
				return fmt.Errorf("%w: local reference inventory budget exceeded", persistence.ErrConflict)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			parts := [][]byte{raw}
			if strings.HasSuffix(path, ".jsonl") {
				parts = bytes.Split(raw, []byte{'\n'})
			}
			for _, part := range parts {
				if strings.HasSuffix(path, ".jsonl") && len(bytes.TrimSpace(part)) == 0 {
					continue
				}
				refs, err := revisiongate.References(part)
				if err != nil {
					return err
				}
				if refs[id] {
					return fmt.Errorf("%w: revision retained by %s", persistence.ErrConflict, path)
				}
			}
			return nil
		})
	}
	roots, err := revisiongate.Consumers(root)
	if err != nil {
		return err
	}
	_, bindingErr := os.Lstat(filepath.Join(s.runsDir, ".sysbox-revision-registry.json"))
	if bindingErr != nil && !os.IsNotExist(bindingErr) {
		return bindingErr
	}
	if bindingErr == nil || len(roots) > 0 || root != filepath.Join(s.runsDir, "global-revisions") {
		found := false
		for _, consumer := range roots {
			if consumer == s.runsDir {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: independent registry consumer manifest missing or incomplete", persistence.ErrConflict)
		}
	}
	if len(roots) == 0 {
		roots = []string{s.runsDir}
	}
	roots = append(roots, root)
	for _, consumer := range roots {
		if consumer != root {
			bound, err := revisiongate.Catalog(consumer)
			if err != nil || bound != root {
				return persistence.ErrConflict
			}
		}
		if err = scan(consumer); err != nil {
			return err
		}
	}
	return nil
}
func compactLocalRevisionRetirement(ctx context.Context, root, id string) error {
	target := filepath.Join(root, id+".json")
	var err error
	tomb := target + ".retired"
	if _, err = os.Lstat(tomb); !os.IsNotExist(err) {
		return persistence.ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Atomic rename retains the original object as permanent retirement proof.
	// A crash after rename fails closed: all participating writes see tombstone.
	if err = os.Rename(target, tomb); err != nil {
		return err
	}
	if err = syncDirectory(root); err != nil {
		return err
	}
	// Once retirement is durable, compact the proof so GC actually releases
	// project bytes. The tombstone filename remains forever to fence replay.
	raw, err := json.Marshal(map[string]any{"revision": id, "retired_at": time.Now().UTC(), "content_digest": id})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".retirement-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), tomb); err != nil {
		return err
	}
	return syncDirectory(root)
}
