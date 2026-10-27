package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// Global revisions are retained regardless of topology/run association. Decode
// file bytes before scanning: JSON's base64 representation hides textual hashes.
func collectGlobalCacheReferences(ctx context.Context, c *sql.Conn, postgres bool, digest hash.Hash) ([]*aggregate, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sysbox_global_revisions')`
	if postgres {
		query = `SELECT to_regclass('sysbox_global_revisions') IS NOT NULL`
	}
	if err := c.QueryRowContext(ctx, query).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	if postgres {
		if _, err := c.ExecContext(ctx, "LOCK TABLE sysbox_global_revisions IN EXCLUSIVE MODE"); err != nil {
			return nil, err
		}
	}
	query = "SELECT revision,files FROM sysbox_global_revisions ORDER BY revision"
	if postgres {
		query = "SELECT id,data FROM sysbox_global_revisions ORDER BY id"
	}
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*aggregate
	total := 0
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		total += len(raw)
		if total > 64<<20 || len(out) >= 10000 {
			return nil, fmt.Errorf("%w: global revision inventory limit exceeded", ErrConflict)
		}
		var rev controlplane.GlobalRevision
		if postgres {
			if err = json.Unmarshal(raw, &rev); err != nil {
				return nil, err
			}
			if rev.Revision != id {
				return nil, ErrCorruptRecord
			}
		} else {
			rev.Revision = id
			if err = json.Unmarshal(raw, &rev.Files); err != nil {
				return nil, err
			}
		}
		if len(rev.Files) == 0 || controlplane.ComputeRevisionDigest(rev.Files) != id {
			return nil, ErrCorruptRecord
		}
		// Synthetic protection-only entry; never stored as lifecycle authority.
		a := newAggregate()
		a.GCSources = map[string]GCSource{"global": {Manifest: GCSourceManifest{Domains: map[string][]string{"revision": {id}}}}}
		source := a.GCSources["global"]
		for _, data := range rev.Files {
			source.Manifest.Domains["artifact"] = append(source.Manifest.Domains["artifact"], fmt.Sprintf("sha256:%x", sha256.Sum256(data)), string(data))
		}
		a.GCSources["global"] = source
		out = append(out, a)
		canonical, err := json.Marshal(rev)
		if err != nil {
			return nil, err
		}
		digest.Write([]byte("global-revision\x00"))
		digest.Write(canonical)
	}
	return out, rows.Err()
}
