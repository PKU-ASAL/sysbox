package persistence

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFormat5ImportIsExplicitAndPreservesUnknownActions(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			repo := resetTestRepository(t, kind)
			continuationFixture(t, repo)
			ctx := context.Background()
			var legacy []byte
			require.NoError(t, repo.update(ctx, "lab", func(a *aggregate) error {
				raw, err := json.Marshal(a)
				if err != nil {
					return err
				}
				var object map[string]json.RawMessage
				if err = json.Unmarshal(raw, &object); err != nil {
					return err
				}
				object["format"] = json.RawMessage("5")
				delete(object, "gc_reservations")
				legacy, err = json.Marshal(object)
				return err
			}))
			plan, err := PrepareFormat5Import(legacy, "lab")
			require.NoError(t, err)
			converted, err := decodeAggregate(plan.Converted)
			require.NoError(t, err)
			require.Empty(t, converted.GC)
			require.NotEmpty(t, converted.Actions)
			for _, action := range converted.Actions {
				require.False(t, action.EffectComplete)
			}
			switch b := repo.backend.(type) {
			case *fileBackend:
				require.NoError(t, os.WriteFile(filepath.Join(b.root, "lab.json"), legacy, 0600))
			case *sqlBackend:
				if b.postgres {
					_, err = b.db.Exec("UPDATE sysbox_lifecycle_aggregates SET data=$1::jsonb WHERE topology='lab'", string(legacy))
					require.NoError(t, err)
					require.NoError(t, b.db.QueryRow("SELECT data FROM sysbox_lifecycle_aggregates WHERE topology='lab'").Scan(&legacy))
					plan, err = PrepareFormat5Import(legacy, "lab")
					require.NoError(t, err)
				} else {
					_, err = b.db.Exec("UPDATE sysbox_lifecycle_aggregates SET data=? WHERE topology='lab'", legacy)
					require.NoError(t, err)
				}
			}
			_, err = repo.LoadState(ctx, "lab")
			require.ErrorIs(t, err, ErrUnsupportedVersion)
			preview, err := repo.PlanFormat5Import(ctx, "lab")
			require.NoError(t, err)
			require.Equal(t, plan.SourceSHA256, preview.SourceSHA256)
			archive := filepath.Join(t.TempDir(), "format5.original.json")
			_, err = repo.ImportFormat5(ctx, "lab", "wrong", archive)
			require.ErrorIs(t, err, ErrConflict)
			_, err = repo.ImportFormat5(ctx, "lab", plan.SourceSHA256, archive)
			require.NoError(t, err)
			saved, err := os.ReadFile(archive)
			require.NoError(t, err)
			require.Equal(t, legacy, saved)
			_, err = repo.LoadState(ctx, "lab")
			require.NoError(t, err)
		})
	}
}
