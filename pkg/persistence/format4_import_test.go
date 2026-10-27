package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func format4TestBytes(t *testing.T, a *aggregate) []byte {
	t.Helper()
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &object))
	object["format"] = json.RawMessage("4")
	delete(object, "reset_continuations")
	delete(object, "gc_reservations")
	raw, err = json.Marshal(object)
	require.NoError(t, err)
	return raw
}

func TestPrepareFormat4ImportPreservesEvidenceAndSnapshots(t *testing.T) {
	r := resetTestRepository(t, "filesystem")
	continuationFixture(t, r)
	ctx := context.Background()
	_, err := r.StateBackend("lab").(*stateBackend).Snapshot(ctx, "retained reset")
	require.NoError(t, err)
	var original *aggregate
	require.NoError(t, r.update(ctx, "lab", func(a *aggregate) error { original = a; return nil }))
	for id, snapshot := range original.Snapshots {
		inner, err := decodeSnapshot(id, snapshot)
		require.NoError(t, err)
		snapshot.Data = format4TestBytes(t, inner)
		snapshot.Digest = fmt.Sprintf("%x", sha256.Sum256(snapshot.Data))
		snapshot.Metadata.Size = int64(len(snapshot.Data))
		original.Snapshots[id] = snapshot
		original.Restores["restore"] = SnapshotRestoreReceipt{Request: SnapshotRestoreRequest{ID: "restore", TopologyUID: original.State.TopologyUID, SnapshotID: id, SnapshotDigest: snapshot.Digest, ExpectedSerial: original.Serial - 1, MaintenanceEpoch: 1}, Serial: original.Serial, CompletedAt: time.Now().UTC()}
	}
	raw := format4TestBytes(t, original)
	before := append([]byte(nil), raw...)
	plan, err := PrepareFormat4Import(raw, "lab")
	require.NoError(t, err)
	require.Equal(t, before, raw)
	converted, err := decodeAggregate(plan.Converted)
	require.NoError(t, err)
	require.Equal(t, original.State, converted.State)
	require.Equal(t, original.Serial, converted.Serial)
	require.Equal(t, original.Actions, converted.Actions)
	require.Equal(t, original.Restores, converted.Restores)
	require.Len(t, converted.Snapshots, len(original.Snapshots))
	for id, snapshot := range converted.Snapshots {
		_, err := decodeSnapshot(id, snapshot)
		require.NoError(t, err)
		require.Equal(t, original.Snapshots[id].Metadata.ID, snapshot.Metadata.ID)
	}
	_, err = PrepareFormat4Import(raw, "foreign")
	require.Error(t, err)
	_, err = PrepareFormat4Import(plan.Converted, "lab")
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	var unknown map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &unknown))
	unknown["unrecognized_authority"] = json.RawMessage("true")
	bad, err := json.Marshal(unknown)
	require.NoError(t, err)
	_, err = PrepareFormat4Import(bad, "lab")
	require.ErrorIs(t, err, ErrCorruptRecord)
	for id, snapshot := range original.Snapshots {
		snapshot.Digest = "corrupt"
		original.Snapshots[id] = snapshot
	}
	_, err = PrepareFormat4Import(format4TestBytes(t, original), "lab")
	require.ErrorIs(t, err, ErrCorruptRecord)
}

func TestFormat4ImportArchivesSourceAndComparesBeforeCommit(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			continuationFixture(t, r)
			ctx := context.Background()
			var old *aggregate
			require.NoError(t, r.update(ctx, "lab", func(a *aggregate) error { old = a; return nil }))
			raw := format4TestBytes(t, old)
			switch b := r.backend.(type) {
			case *fileBackend:
				require.NoError(t, os.WriteFile(filepath.Join(b.root, "lab.json"), raw, 0600))
			case *sqlBackend:
				query := `UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology='lab'`
				var value any = raw
				if b.postgres {
					query, value = `UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology='lab'`, string(raw)
				}
				_, err := b.db.ExecContext(ctx, query, value)
				require.NoError(t, err)
			}
			_, err := r.LoadState(ctx, "lab")
			require.ErrorIs(t, err, ErrUnsupportedVersion)
			plan, err := r.PlanFormat4Import(ctx, "lab")
			require.NoError(t, err)
			archive := filepath.Join(t.TempDir(), "original.format4.json")
			_, err = r.ImportFormat4(ctx, "lab", "wrong-hash", archive)
			require.ErrorIs(t, err, ErrConflict)
			_, err = os.Stat(archive)
			require.True(t, os.IsNotExist(err))
			require.NoError(t, os.WriteFile(archive, []byte("unrelated archive"), 0600))
			_, err = r.ImportFormat4(ctx, "lab", plan.SourceSHA256, archive)
			require.ErrorIs(t, err, ErrConflict)
			_, err = r.LoadState(ctx, "lab")
			require.ErrorIs(t, err, ErrUnsupportedVersion, "archive failure must not change the source")
			require.NoError(t, os.Remove(archive))
			if b, ok := r.backend.(*fileBackend); ok {
				_, err = r.ImportFormat4(ctx, "lab", plan.SourceSHA256, filepath.Join(b.root, "lab.json"))
				require.ErrorIs(t, err, ErrConflict, "the live source cannot serve as its own backup")
			}
			imported, err := r.ImportFormat4(ctx, "lab", plan.SourceSHA256, archive)
			require.NoError(t, err)
			require.Equal(t, plan.SourceSHA256, imported.SourceSHA256)
			backup, err := os.ReadFile(archive)
			require.NoError(t, err)
			require.Equal(t, plan.SourceSHA256, fmt.Sprintf("%x", sha256.Sum256(backup)))
			st, err := r.LoadState(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, old.State.TopologyUID, st.TopologyUID)
			require.Equal(t, old.Serial, st.Meta.Serial)
			actions, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, actions, len(old.Actions))
			for _, action := range actions {
				require.Equal(t, old.Actions[action.ID], action)
			}
		})
	}
}

func TestFormat4ImportRejectsArchiveInTopologyCatalog(t *testing.T) {
	r := resetTestRepository(t, "filesystem")
	continuationFixture(t, r)
	var old *aggregate
	require.NoError(t, r.update(context.Background(), "lab", func(a *aggregate) error { old = a; return nil }))
	b := r.backend.(*fileBackend)
	raw := format4TestBytes(t, old)
	source := filepath.Join(b.root, "lab.json")
	require.NoError(t, os.WriteFile(source, raw, 0600))
	plan, err := r.PlanFormat4Import(context.Background(), "lab")
	require.NoError(t, err)
	_, err = r.ImportFormat4(context.Background(), "lab", plan.SourceSHA256, filepath.Join(b.root, "archive.json"))
	require.ErrorIs(t, err, ErrConflict)
	after, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, raw, after)
}

func TestImportSourceDoesNotBootstrapSQL(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			b := r.backend.(*sqlBackend)
			_, err := b.db.Exec(`DROP TABLE sysbox_lifecycle_aggregates`)
			require.NoError(t, err)
			backendURL := "sqlite://" + b.path
			if b.postgres {
				backendURL = b.dsn
			}
			source, err := OpenImportSource(context.Background(), backendURL, "")
			require.NoError(t, err)
			defer source.Close()
			_, err = source.PlanFormat4Import(context.Background(), "lab")
			require.Error(t, err)
			query := `SELECT NOT EXISTS(SELECT 1 FROM sqlite_master WHERE name='sysbox_lifecycle_aggregates')`
			if b.postgres {
				query = `SELECT to_regclass('sysbox_lifecycle_aggregates') IS NULL`
			}
			var absent bool
			require.NoError(t, b.db.QueryRow(query).Scan(&absent))
			require.True(t, absent, "preview must not bootstrap schema")
		})
	}
	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := OpenImportSource(context.Background(), "sqlite://"+path, "")
	require.Error(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "preview must not create a database")
}
