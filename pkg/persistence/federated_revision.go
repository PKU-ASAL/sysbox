package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// RetireFederatedRevisions is an explicit irreversible retirement at one SQL
// consumer. A coordinator must collect every enrolled consumer's durable ack
// before retiring its own registry. Failures retain conservative local fences;
// retries only confirm the same idempotency key, never remove those fences.
func (r *Repository) RetireFederatedRevisions(ctx context.Context, registry, request string, ids []string, olderThan time.Time) error {
	if !topologyName.MatchString(registry) || !topologyName.MatchString(request) || len(ids) < 1 || len(ids) > 128 || olderThan.IsZero() || olderThan.After(time.Now()) {
		return ErrInvalidRecord
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for i, id := range ordered {
		if len(id) != 71 || retainedRevisionDigest.FindString(id) != id || (i > 0 && ordered[i-1] == id) {
			return ErrInvalidRecord
		}
	}
	raw, _ := json.Marshal(ordered)
	hash := evidenceHash(json.RawMessage(raw))
	b, ok := r.backend.(*sqlBackend)
	if !ok || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	return r.cacheCatalogTransaction(ctx, true, func(c *sql.Conn, all []*aggregate, _ string) error {
		if _, err := c.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sysbox_federated_retirements (registry TEXT NOT NULL,request TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(registry,request))`); err != nil {
			return err
		}
		query := "SELECT digest FROM sysbox_federated_retirements WHERE registry=? AND request=?"
		if b.postgres {
			query = "SELECT digest FROM sysbox_federated_retirements WHERE registry=$1 AND request=$2"
		}
		var prior string
		err := c.QueryRowContext(ctx, query, registry, request).Scan(&prior)
		if err == nil {
			if prior != hash {
				return ErrConflict
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if b.postgres {
			if _, err = c.ExecContext(ctx, "LOCK TABLE sysbox_runs IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
		}
		for _, id := range ordered {
			// A previously retired ID can acknowledge a later exact retirement intent,
			// but absence without a durable fence never establishes safety by itself.
			var exists bool
			query = "SELECT EXISTS(SELECT 1 FROM sysbox_global_revisions WHERE revision=?)"
			if b.postgres {
				query = "SELECT EXISTS(SELECT 1 FROM sysbox_global_revisions WHERE id=$1)"
			}
			if err = c.QueryRowContext(ctx, query, id).Scan(&exists); err != nil {
				return err
			}
			if exists {
				if err = deleteGlobalRevisionLocked(ctx, c, b.postgres, all, id, olderThan); err != nil {
					return err
				}
				continue
			}
			query = "SELECT EXISTS(SELECT 1 FROM sysbox_runs WHERE revision=?)"
			if b.postgres {
				query = "SELECT EXISTS(SELECT 1 FROM sysbox_runs WHERE data->>'revision'=$1)"
			}
			var referenced bool
			if err = c.QueryRowContext(ctx, query, id).Scan(&referenced); err != nil {
				return err
			}
			if referenced {
				return fmt.Errorf("%w: retained Run revision", ErrConflict)
			}
			for _, a := range all {
				yes, err := cacheReferenced(a, []string{id})
				if err != nil {
					return err
				}
				if yes {
					return fmt.Errorf("%w: retained revision", ErrConflict)
				}
			}
			if err = retainedAPIRevision(ctx, c, b.postgres, id); err != nil {
				return err
			}
			if err = installRevisionAdmissionGuard(ctx, c, b.postgres); err != nil {
				return err
			}
			if err = installRevisionRetirementGuards(ctx, c, b.postgres, id); err != nil {
				return err
			}
		}
		query = "INSERT INTO sysbox_federated_retirements(registry,request,digest) VALUES(?,?,?)"
		if b.postgres {
			query = "INSERT INTO sysbox_federated_retirements(registry,request,digest) VALUES($1,$2,$3)"
		}
		_, err = c.ExecContext(ctx, query, registry, request, hash)
		return err
	})
}
