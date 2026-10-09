package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fault injection deliberately bypasses the production write validator. The
// read/CAS assertions must still reject data corrupted after a valid commit.
func injectGuestRecordForTest(t *testing.T, records guestRecords, kind guestKind, id string, raw []byte) {
	t.Helper()
	ctx := context.Background()
	switch b := records.(type) {
	case *fileGuestRecords:
		dir, err := b.directory(kind)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, writePrivateGuestRecord(ctx, filepath.Join(dir, id+".json"), raw))
	case *sqliteGuestRecords:
		_, err := b.db.ExecContext(ctx, "INSERT INTO "+string(kind)+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, raw)
		require.NoError(t, err)
	case *postgresGuestRecords:
		_, err := b.pool.Exec(ctx, "INSERT INTO "+string(kind)+"(id,data) VALUES($1,$2::jsonb) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, string(raw))
		require.NoError(t, err)
	default:
		t.Fatal("unsupported corruption fixture store")
	}
}
