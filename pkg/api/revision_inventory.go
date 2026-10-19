package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

const maxRevisionInventory = 10000
const maxRevisionInventoryBytes = 64 << 20

func (s *Server) globalRevisionInventory(ctx context.Context) ([]controlplane.GlobalRevision, error) {
	out := []controlplane.GlobalRevision{}
	total := 0
	appendRevision := func(id string, rev controlplane.GlobalRevision, rawSize int) error {
		total += rawSize
		if len(out) >= maxRevisionInventory || total > maxRevisionInventoryBytes {
			return fmt.Errorf("revision inventory exceeds bounded limit")
		}
		if rev.Revision != id || len(rev.Files) == 0 || controlplane.ComputeRevisionDigest(rev.Files) != id || rev.CreatedAt.IsZero() {
			return persistence.ErrCorruptRecord
		}
		out = append(out, rev)
		return nil
	}
	switch store := s.apiStore.(type) {
	case *localAPIStore:
		root := filepath.Join(store.runsDir, "global-revisions")
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > maxRevisionInventoryBytes {
				return nil, persistence.ErrCorruptRecord
			}
			rev, err := readLocalObject[controlplane.GlobalRevision](filepath.Join(root, entry.Name()))
			if err != nil {
				return nil, err
			}
			if err = appendRevision(strings.TrimSuffix(entry.Name(), ".json"), rev, int(info.Size())); err != nil {
				return nil, err
			}
		}
	case *sqliteAPIStore:
		db, err := store.open()
		if err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, "SELECT revision,files,size,created_at FROM sysbox_global_revisions ORDER BY revision LIMIT 10001")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var rev controlplane.GlobalRevision
			var raw []byte
			var at string
			if err = rows.Scan(&rev.Revision, &raw, &rev.Size, &at); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &rev.Files); err != nil {
				return nil, err
			}
			rev.CreatedAt = parseSQLiteTime(at)
			if err = appendRevision(rev.Revision, rev, len(raw)); err != nil {
				return nil, err
			}
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
	case *postgresAPIStore:
		conn, err := store.connect(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Release()
		rows, err := conn.Query(ctx, "SELECT id,data::text FROM sysbox_global_revisions ORDER BY id LIMIT 10001")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				return nil, err
			}
			var rev controlplane.GlobalRevision
			if err = json.Unmarshal(raw, &rev); err != nil {
				return nil, err
			}
			if err = appendRevision(id, rev, len(raw)); err != nil {
				return nil, err
			}
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
	default:
		return nil, persistence.ErrUnsupportedVersion
	}
	return out, nil
}
func (s *Server) lifecycleRevisionReferences(ctx context.Context) (map[string]bool, error) {
	refs := map[string]bool{}
	collect := func(repo *persistence.Repository, name string) error {
		items, err := repo.LoadRevisionReferences(ctx, name)
		if err != nil {
			return err
		}
		for id := range items {
			refs[id] = true
		}
		return nil
	}
	if s.stateBackend != "" {
		u, parseErr := url.Parse(s.stateBackend)
		if parseErr != nil {
			return nil, parseErr
		}
		if selector := u.Query().Get("topology"); selector != "" && selector != "{topology}" {
			return nil, fmt.Errorf("global revision retention requires an unscoped lifecycle catalog")
		}
		repo, err := persistence.OpenCatalog(ctx, s.stateBackend)
		if err != nil {
			return nil, err
		}
		defer repo.Close()
		topologies, err := repo.ListTopologies(ctx)
		if err != nil {
			return nil, err
		}
		if len(topologies) > 10000 {
			return nil, persistence.ErrConflict
		}
		for _, entry := range topologies {
			if err = collect(repo, entry.Name); err != nil {
				return nil, err
			}
		}
	} else {
		entries, err := os.ReadDir(s.runsDir)
		if os.IsNotExist(err) {
			return refs, nil
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || validatePathSegment(entry.Name(), "topology") != nil {
				continue
			}
			if _, err = os.Lstat(filepath.Join(s.runsDir, entry.Name(), "state.json")); err == nil {
				return nil, persistence.ErrUnsupportedVersion
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			path := filepath.Join(s.runsDir, entry.Name(), "state.json.lifecycle")
			if _, err = os.Stat(path); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, err
			}
			repo, err := s.actionRepository(entry.Name())
			if err != nil {
				return nil, err
			}
			if err = collect(repo, entry.Name()); err != nil {
				return nil, err
			}
		}
	}
	return refs, nil
}
