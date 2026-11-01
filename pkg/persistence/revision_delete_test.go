package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRevisionDeletionAndRunAdmissionShareTransaction(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			db := r.backend.(*sqlBackend).db
			if kind == "sqlite" {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT,created_at TEXT); CREATE TABLE sysbox_runs(id TEXT,revision TEXT)`)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(id TEXT PRIMARY KEY,data JSONB); CREATE TABLE sysbox_runs(id TEXT,data JSONB)`)
				require.NoError(t, err)
			}
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
			require.NoError(t, err)
			require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
			files := map[string][]byte{"field.sysbox.hcl": []byte("unused")}
			id := controlplane.ComputeRevisionDigest(files)
			at := time.Now().UTC().Add(-time.Hour)
			if kind == "sqlite" {
				raw, _ := json.Marshal(files)
				_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES(?,?,?)`, id, string(raw), at.Format(time.RFC3339Nano))
			} else {
				raw, _ := json.Marshal(controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: at})
				_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES($1,$2::jsonb)`, id, string(raw))
			}
			require.NoError(t, err)
			require.NoError(t, r.DeleteGlobalRevision(ctx, id, time.Now()))
			if kind == "sqlite" {
				_, err = db.Exec(`INSERT INTO sysbox_runs VALUES('late',?)`, id)
			} else {
				_, err = db.Exec(`INSERT INTO sysbox_runs VALUES('late',jsonb_build_object('revision',$1::text))`, id)
			}
			require.Error(t, err, "late admission cannot reference deleted revision")
		})
	}
}

func TestRevisionDeletionRacesIndependentRunWriter(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			b := r.backend.(*sqlBackend)
			db := b.db
			if kind == "sqlite" {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT,created_at TEXT); CREATE TABLE sysbox_runs(id TEXT,revision TEXT)`)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(id TEXT PRIMARY KEY,data JSONB); CREATE TABLE sysbox_runs(id TEXT,data JSONB)`)
				require.NoError(t, err)
			}
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
			require.NoError(t, err)
			require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
			var writer *Repository
			if kind == "sqlite" {
				writer, err = OpenSQLite(b.path)
			} else {
				writer, err = OpenPostgres(ctx, b.dsn)
			}
			require.NoError(t, err)
			defer writer.Close()
			other := writer.backend.(*sqlBackend).db
			for i := 0; i < 8; i++ {
				files := map[string][]byte{"field.sysbox.hcl": []byte(fmt.Sprintf("revision %d", i))}
				id := controlplane.ComputeRevisionDigest(files)
				at := time.Now().UTC().Add(-time.Hour)
				if kind == "sqlite" {
					raw, _ := json.Marshal(files)
					_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES(?,?,?)`, id, string(raw), at.Format(time.RFC3339Nano))
				} else {
					raw, _ := json.Marshal(controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: at})
					_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES($1,$2::jsonb)`, id, string(raw))
				}
				require.NoError(t, err)
				start := make(chan struct{})
				deleted := make(chan error, 1)
				admitted := make(chan error, 1)
				go func() { <-start; deleted <- r.DeleteGlobalRevision(ctx, id, time.Now()) }()
				go func() {
					<-start
					var err error
					if kind == "sqlite" {
						_, err = other.Exec(`INSERT INTO sysbox_runs VALUES(?,?)`, fmt.Sprint(i), id)
					} else {
						_, err = other.Exec(`INSERT INTO sysbox_runs VALUES($1,jsonb_build_object('revision',$2::text))`, fmt.Sprint(i), id)
					}
					admitted <- err
				}()
				close(start)
				d, a := <-deleted, <-admitted
				require.False(t, d == nil && a == nil, "delete and new reference cannot both commit")
				if a == nil {
					require.Error(t, r.DeleteGlobalRevision(ctx, id, time.Now()), "committed historical run protects revision")
				}
			}
		})
	}
}

func TestRevisionDeletionForcesReadCommittedCatalog(t *testing.T) {
	r := resetTestRepository(t, "postgres")
	b := r.backend.(*sqlBackend)
	c, err := b.db.Conn(context.Background())
	require.NoError(t, err)
	_, err = c.ExecContext(context.Background(), "SET default_transaction_isolation='repeatable read'")
	require.NoError(t, err)
	require.NoError(t, c.Close())
	// The transaction's first read must be explicitly read committed even on
	// sessions configured otherwise, not inherit a stale snapshot.
	st, err := r.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	v, err := r.BeginGCSource(context.Background(), "lab", "host", st.TopologyUID, 0)
	require.NoError(t, err)
	require.NoError(t, r.CompleteGCSource(context.Background(), "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
	require.NoError(t, r.cacheCatalogTransaction(context.Background(), false, func(c *sql.Conn, _ []*aggregate, _ string) error {
		var level string
		require.NoError(t, c.QueryRowContext(context.Background(), "SHOW transaction_isolation").Scan(&level))
		require.Equal(t, "read committed", level)
		return nil
	}))
}
