package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

// DeleteGlobalRevision is an explicit single-object retirement, not permission
// derived from a non-atomic preview. It rechecks all references under SQL locks.
// No filesystem registry deletion is supported by this transactional path.
func (r *Repository) DeleteGlobalRevision(ctx context.Context, id string, olderThan time.Time) error {
	if !retainedRevisionDigest.MatchString(id) || len(id) != 71 || olderThan.IsZero() || olderThan.After(time.Now()) {
		return ErrInvalidRecord
	}
	b, ok := r.backend.(*sqlBackend)
	if !ok || r.boundTopology != "" {
		return ErrUnsupportedVersion
	}
	return r.cacheCatalogTransaction(ctx, true, func(c *sql.Conn, all []*aggregate, _ string) error {
		// cacheCatalogTransaction already locks lifecycle and global revisions.
		// Run publishers may already hold their table lock; this is why the guard
		// uses statement triggers with a fresh READ COMMITTED visibility check.
		if b.postgres {
			if _, err := c.ExecContext(ctx, "LOCK TABLE sysbox_runs IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
		}
		query := "SELECT files,created_at FROM sysbox_global_revisions WHERE revision=?"
		var rev controlplane.GlobalRevision
		if b.postgres {
			var raw []byte
			if err := c.QueryRowContext(ctx, "SELECT data FROM sysbox_global_revisions WHERE id=$1", id).Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &rev); err != nil {
				return err
			}
		} else {
			var raw []byte
			var at string
			if err := c.QueryRowContext(ctx, query, id).Scan(&raw, &at); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &rev.Files); err != nil {
				return err
			}
			parsed, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return err
			}
			rev.CreatedAt = parsed
			rev.Revision = id
		}
		if rev.Revision != id || controlplane.ComputeRevisionDigest(rev.Files) != id || !rev.CreatedAt.Before(olderThan) {
			return ErrConflict
		}
		var referenced bool
		query = "SELECT EXISTS(SELECT 1 FROM sysbox_runs WHERE revision=?)"
		if b.postgres {
			query = "SELECT EXISTS(SELECT 1 FROM sysbox_runs WHERE data->>'revision'=$1)"
		}
		if err := c.QueryRowContext(ctx, query, id).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			return fmt.Errorf("%w: revision retained by Run history", ErrConflict)
		}
		for _, a := range all {
			// Synthetic global content protects other objects, but a revision's own
			// registry record cannot count as its own lifecycle reference.
			if a.GCSources != nil {
				if _, synthetic := a.GCSources["global"]; synthetic && a.Serial == 0 && len(a.State.Resources) == 0 && a.Format == FormatVersion {
					if source := a.GCSources["global"]; len(source.Manifest.Domains["revision"]) == 1 && source.Manifest.Domains["revision"][0] == id {
						continue
					}
				}
			}
			yes, err := cacheReferenced(a, []string{id})
			if err != nil {
				return err
			}
			if yes {
				return fmt.Errorf("%w: retained lifecycle revision", ErrConflict)
			}
		}
		if err := retainedAPIRevision(ctx, c, b.postgres, id); err != nil {
			return err
		}
		if err := installRevisionAdmissionGuard(ctx, c, b.postgres); err != nil {
			return err
		}
		if err := installRevisionRetirementGuards(ctx, c, b.postgres, id); err != nil {
			return err
		}
		query = "DELETE FROM sysbox_global_revisions WHERE revision=?"
		if b.postgres {
			query = "DELETE FROM sysbox_global_revisions WHERE id=$1"
		}
		result, err := c.ExecContext(ctx, query, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
}
func installRevisionAdmissionGuard(ctx context.Context, c *sql.Conn, postgres bool) error {
	var statements []string
	if postgres {
		statements = []string{
			`CREATE OR REPLACE FUNCTION sysbox_revision_run_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE rev text; present boolean; BEGIN rev := NEW.data->>'revision'; IF rev IS NULL OR rev='' THEN RETURN NEW; END IF; IF TG_OP='UPDATE' AND OLD.data->>'revision'=rev THEN RETURN NEW; END IF; IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'revision admission requires read committed' USING ERRCODE='55000'; END IF; EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.sysbox_global_revisions WHERE id=$1)',TG_TABLE_SCHEMA) INTO present USING rev; IF NOT present THEN RAISE EXCEPTION 'Run revision is absent' USING ERRCODE='23503'; END IF; RETURN NEW; END $$`,
			`DROP TRIGGER IF EXISTS sysbox_revision_run_guard ON sysbox_runs`,
			`CREATE TRIGGER sysbox_revision_run_guard BEFORE INSERT OR UPDATE ON sysbox_runs FOR EACH ROW EXECUTE FUNCTION sysbox_revision_run_guard()`,
		}
	} else {
		statements = []string{
			`CREATE TRIGGER IF NOT EXISTS sysbox_revision_run_insert BEFORE INSERT ON sysbox_runs WHEN NEW.revision IS NOT NULL AND NEW.revision<>'' AND NOT EXISTS(SELECT 1 FROM sysbox_global_revisions WHERE revision=NEW.revision) BEGIN SELECT RAISE(ABORT,'Run revision is absent'); END`,
			`CREATE TRIGGER IF NOT EXISTS sysbox_revision_run_update BEFORE UPDATE OF revision ON sysbox_runs WHEN NEW.revision IS NOT NULL AND NEW.revision<>'' AND NEW.revision IS NOT OLD.revision AND NOT EXISTS(SELECT 1 FROM sysbox_global_revisions WHERE revision=NEW.revision) BEGIN SELECT RAISE(ABORT,'Run revision is absent'); END`,
		}
	}
	for _, statement := range statements {
		if _, err := c.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func retainedAPIRevision(ctx context.Context, c *sql.Conn, postgres bool, id string) error {
	for _, table := range []string{"sysbox_agent_commands", "sysbox_checkpoints", "sysbox_plans"} {
		var exists bool
		if postgres {
			if err := c.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
				return err
			}
		} else {
			if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists); err != nil {
				return err
			}
		}
		if !exists {
			continue
		}
		if postgres {
			if _, err := c.ExecContext(ctx, "LOCK TABLE "+table+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
		}
		rows, err := c.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 10001")
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		count, total := 0, 0
		for rows.Next() {
			count++
			if count > 10000 {
				rows.Close()
				return ErrConflict
			}
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			for _, value := range values {
				var text string
				switch v := value.(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				}
				total += len(text)
				if total > 64<<20 {
					rows.Close()
					return ErrConflict
				}
				if strings.Contains(text, id) {
					rows.Close()
					return fmt.Errorf("%w: revision retained by %s", ErrConflict, table)
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Durable tombstones protect auxiliary references created outside Run
// admission. Republishing bytes does not silently revive retired references.
func installRevisionRetirementGuards(ctx context.Context, c *sql.Conn, postgres bool, id string) error {
	if _, err := c.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS sysbox_retired_revisions (revision TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	insert := "INSERT INTO sysbox_retired_revisions(revision) VALUES(?) ON CONFLICT DO NOTHING"
	if postgres {
		insert = "INSERT INTO sysbox_retired_revisions(revision) VALUES($1) ON CONFLICT DO NOTHING"
	}
	if _, err := c.ExecContext(ctx, insert, id); err != nil {
		return err
	}
	for _, table := range []string{"sysbox_lifecycle_aggregates", "sysbox_runs", "sysbox_agent_commands", "sysbox_checkpoints", "sysbox_plans"} {
		var exists bool
		if postgres {
			if err := c.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
				return err
			}
		} else {
			if err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists); err != nil {
				return err
			}
		}
		if !exists {
			continue
		}
		if postgres {
			statements := []string{
				`CREATE OR REPLACE FUNCTION sysbox_retired_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE blocked boolean; BEGIN IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'revision admission requires read committed' USING ERRCODE='55000'; END IF; EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.sysbox_retired_revisions WHERE strpos($1,revision)>0)',TG_TABLE_SCHEMA) INTO blocked USING to_jsonb(NEW)::text; IF blocked THEN RAISE EXCEPTION 'retired revision reference' USING ERRCODE='23503'; END IF; RETURN NEW; END $$`,
				"DROP TRIGGER IF EXISTS sysbox_retired_revision_guard ON " + table,
				"CREATE TRIGGER sysbox_retired_revision_guard BEFORE INSERT OR UPDATE ON " + table + " FOR EACH ROW EXECUTE FUNCTION sysbox_retired_revision_guard()",
			}
			for _, q := range statements {
				if _, err := c.ExecContext(ctx, q); err != nil {
					return err
				}
			}
		} else {
			rows, err := c.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			rows.Close()
			if err != nil {
				return err
			}
			terms := []string{}
			for _, column := range columns {
				terms = append(terms, "instr(CAST(NEW.\""+strings.ReplaceAll(column, "\"", "\"\"")+"\" AS TEXT),revision)>0")
			}
			for _, op := range []string{"INSERT", "UPDATE"} {
				q := "CREATE TRIGGER IF NOT EXISTS sysbox_retired_" + table + "_" + strings.ToLower(op) + " BEFORE " + op + " ON " + table + " WHEN EXISTS(SELECT 1 FROM sysbox_retired_revisions WHERE " + strings.Join(terms, " OR ") + ") BEGIN SELECT RAISE(ABORT,'retired revision reference'); END"
				if _, err := c.ExecContext(ctx, q); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
