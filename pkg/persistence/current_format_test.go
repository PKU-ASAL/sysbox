package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleUsesOneCurrentFormatAndRejectsPriorEvidence(t *testing.T) {
	require.Equal(t, 16, FormatVersion)
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			repo := resetTestRepository(t, kind)
			ctx := context.Background()
			original, err := json.Marshal(newAggregate())
			require.NoError(t, err)
			for version := 1; version < FormatVersion; version++ {
				var object map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(original, &object))
				object["format"] = json.RawMessage(fmt.Sprint(version))
				raw, err := json.Marshal(object)
				require.NoError(t, err)
				switch b := repo.backend.(type) {
				case *fileBackend:
					require.NoError(t, os.WriteFile(filepath.Join(b.root, "lab.json"), raw, 0600))
				case *sqlBackend:
					if b.postgres {
						_, err = b.db.Exec("INSERT INTO sysbox_lifecycle_aggregates(topology,data) VALUES($1,$2::jsonb) ON CONFLICT(topology) DO UPDATE SET data=EXCLUDED.data", "lab", string(raw))
					} else {
						_, err = b.db.Exec("INSERT OR REPLACE INTO sysbox_lifecycle_aggregates(topology,data) VALUES(?,?)", "lab", raw)
					}
					require.NoError(t, err)
				}
				_, err = repo.LoadState(ctx, "lab")
				require.ErrorIs(t, err, ErrUnsupportedVersion)
				var after []byte
				switch b := repo.backend.(type) {
				case *fileBackend:
					after, err = os.ReadFile(filepath.Join(b.root, "lab.json"))
				case *sqlBackend:
					err = b.db.QueryRow("SELECT data FROM sysbox_lifecycle_aggregates WHERE topology='lab'").Scan(&after)
				}
				require.NoError(t, err)
				require.JSONEq(t, string(raw), string(after))
			}
		})
	}
}
