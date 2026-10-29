package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRevisionRetentionBatchIsAtomicAndKeepsNewest(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			r := resetTestRepository(t, kind)
			db := r.backend.(*sqlBackend).db
			if kind == "sqlite" {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(revision TEXT PRIMARY KEY,files TEXT,created_at TEXT);CREATE TABLE sysbox_runs(id TEXT,revision TEXT)`)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`CREATE TABLE sysbox_global_revisions(id TEXT PRIMARY KEY,data JSONB);CREATE TABLE sysbox_runs(id TEXT,data JSONB)`)
				require.NoError(t, err)
			}
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			v, err := r.BeginGCSource(ctx, "lab", "host", st.TopologyUID, 0)
			require.NoError(t, err)
			require.NoError(t, r.CompleteGCSource(ctx, "lab", "host", st.TopologyUID, v, GCSourceManifest{Domains: map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}, ValidUntil: time.Now().Add(time.Hour)}))
			ids := []string{}
			now := time.Now().UTC()
			for i := 0; i < 3; i++ {
				files := map[string][]byte{"field.sysbox.hcl": []byte(fmt.Sprint(i))}
				id := controlplane.ComputeRevisionDigest(files)
				ids = append(ids, id)
				at := now.Add(time.Duration(i-10) * time.Hour)
				if kind == "sqlite" {
					raw, _ := json.Marshal(files)
					_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES(?,?,?)`, id, string(raw), at.Format(time.RFC3339Nano))
				} else {
					raw, _ := json.Marshal(controlplane.GlobalRevision{Revision: id, Files: files, CreatedAt: at})
					_, err = db.Exec(`INSERT INTO sysbox_global_revisions VALUES($1,$2::jsonb)`, id, string(raw))
				}
				require.NoError(t, err)
			}
			require.Error(t, r.DeleteGlobalRevisionBatch(ctx, []string{ids[0], ids[2]}, now, 1))
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM sysbox_global_revisions").Scan(&count))
			require.Equal(t, 3, count)
			if kind == "sqlite" {
				_, err = db.Exec(`INSERT INTO sysbox_runs VALUES('retain',?)`, ids[1])
			} else {
				_, err = db.Exec(`INSERT INTO sysbox_runs VALUES('retain',jsonb_build_object('revision',$1::text))`, ids[1])
			}
			require.NoError(t, err)
			require.Error(t, r.DeleteGlobalRevisionBatch(ctx, ids[:2], now, 1))
			require.NoError(t, db.QueryRow("SELECT count(*) FROM sysbox_global_revisions").Scan(&count))
			require.Equal(t, 3, count, "a later protected member rolls back earlier deletion and guards")
			var guardCount int
			if kind == "sqlite" {
				require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='sysbox_retired_revisions'").Scan(&guardCount))
			} else {
				require.NoError(t, db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='sysbox_retired_revisions'").Scan(&guardCount))
			}
			require.Zero(t, guardCount, "failed batch must roll back retirement table and triggers")
			require.NoError(t, r.DeleteGlobalRevisionBatch(ctx, ids[:1], now, 1))
			require.NoError(t, db.QueryRow("SELECT count(*) FROM sysbox_global_revisions").Scan(&count))
			require.Equal(t, 2, count)
			require.Error(t, r.DeleteGlobalRevisionBatch(ctx, []string{ids[1], ids[1]}, now, 0))
		})
	}
}

func TestRevisionRetentionBatchRejectsInvalidBoundsBeforeStorage(t *testing.T) {
	r, err := OpenFilesystem(t.TempDir())
	require.NoError(t, err)
	defer r.Close()
	id := "sha256:" + fmt.Sprintf("%064d", 1)
	for _, ids := range [][]string{nil, {id, id}, make([]string, 129)} {
		require.ErrorIs(t, r.DeleteGlobalRevisionBatch(context.Background(), ids, time.Now(), 1), ErrInvalidRecord)
	}
	require.ErrorIs(t, r.DeleteGlobalRevisionBatch(context.Background(), []string{id}, time.Now(), -1), ErrInvalidRecord)
	require.ErrorIs(t, r.DeleteGlobalRevisionBatch(context.Background(), []string{id}, time.Now().Add(time.Hour), 1), ErrInvalidRecord)
}
