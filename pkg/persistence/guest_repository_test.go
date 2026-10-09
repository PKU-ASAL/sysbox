package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func guestRepositoryFactories(t *testing.T, test func(*testing.T, func() *GuestRepository)) {
	t.Helper()
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if kind == "filesystem" {
				test(t, func() *GuestRepository { return NewFilesystemGuestRepository(root) })
				return
			}
			if kind == "sqlite" {
				test(t, func() *GuestRepository {
					db, err := sql.Open("sqlite", filepath.Join(root, "guest.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, db.Close()) })
					_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sysbox_guest_executions(id TEXT PRIMARY KEY,data BLOB NOT NULL);CREATE TABLE IF NOT EXISTS sysbox_guest_file_operations(id TEXT PRIMARY KEY,data BLOB NOT NULL);CREATE TABLE IF NOT EXISTS sysbox_execution_authority(id TEXT PRIMARY KEY,data BLOB NOT NULL)`)
					require.NoError(t, err)
					return NewSQLiteGuestRepository(db)
				})
				return
			}
			dsn := os.Getenv("SYSBOX_TEST_POSTGRES_DSN")
			if dsn == "" {
				t.Skip("isolated Postgres test server not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			admin, err := pgx.Connect(ctx, dsn)
			require.NoError(t, err)
			schema := "guest_contract_" + uuid.NewString()
			quoted := pgx.Identifier{schema}.Sanitize()
			_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
				defer done()
				_, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE")
				require.NoError(t, err)
				require.NoError(t, admin.Close(cleanup))
			})
			config, err := pgxpool.ParseConfig(dsn)
			require.NoError(t, err)
			config.ConnConfig.RuntimeParams["search_path"] = quoted
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			_, err = pool.Exec(ctx, `CREATE TABLE sysbox_guest_executions(id TEXT PRIMARY KEY,data JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());CREATE TABLE sysbox_guest_file_operations(id TEXT PRIMARY KEY,data JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());CREATE TABLE sysbox_execution_authority(id TEXT PRIMARY KEY,data JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
			require.NoError(t, err)
			test(t, func() *GuestRepository { return NewPostgresGuestRepository(pool) })
		})
	}
}
func executionFixture(id string) controlplane.GuestExecution {
	return controlplane.GuestExecution{ID: id, Version: 1, Topology: "lab", Node: "node", AgentID: "agent", Status: controlplane.GuestExecutionQueued, Request: controlplane.GuestExecutionRequest{Argv: []string{"run"}, Environment: map[string]string{"SECRET": "private-value"}}}
}
func TestGuestRepositoryDurableContract(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		ctx := context.Background()
		r := open()
		x := executionFixture("exec")
		require.NoError(t, r.SaveGuestExecution(ctx, x))
		require.NoError(t, r.SaveGuestExecution(ctx, x))
		again := open()
		got, err := again.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, x, *got)
		public, err := json.Marshal(got)
		require.NoError(t, err)
		require.NotContains(t, string(public), "private-value")
		require.NotContains(t, string(public), "agent")
		x.Status = controlplane.GuestExecutionRunning
		x.CommandID = "command"
		ok, err := r.CompareAndSwapGuestExecution(ctx, x, 1)
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = again.CompareAndSwapGuestExecution(ctx, x, 1)
		require.NoError(t, err)
		require.False(t, ok)
		got, err = again.GetGuestExecution(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, int64(2), got.Version)
		require.Equal(t, "command", got.CommandID)
		got.CommandID = "other-command"
		ok, err = r.CompareAndSwapGuestExecution(ctx, *got, 2)
		require.ErrorIs(t, err, ErrConflict)
		require.False(t, ok)
		_, err = r.GetGuestExecution(ctx, "absent")
		require.ErrorIs(t, err, ErrNotFound)
		missing := executionFixture("absent")
		ok, err = r.CompareAndSwapGuestExecution(ctx, missing, 1)
		require.ErrorIs(t, err, ErrNotFound)
		require.False(t, ok)
		_, err = r.GetGuestExecution(ctx, "absent")
		require.ErrorIs(t, err, ErrNotFound, "missing CAS must not publish a placeholder")
		for _, version := range []int64{0, -1, math.MaxInt64} {
			x.Version = version
			ok, err = r.CompareAndSwapGuestExecution(ctx, x, version)
			require.Error(t, err)
			require.False(t, ok)
		}
	})
}
func TestGuestRepositoryConcurrentCASHasOneWinner(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		ctx := context.Background()
		a, b := open(), open()
		initial := executionFixture("race")
		require.NoError(t, a.SaveGuestExecution(ctx, initial))
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan bool, 2)
		errs := make(chan error, 2)
		for _, repo := range []*GuestRepository{a, b} {
			wg.Go(func() {
				<-start
				next := initial
				next.Status = controlplane.GuestExecutionRunning
				ok, err := repo.CompareAndSwapGuestExecution(ctx, next, 1)
				results <- ok
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		for err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, 1, winners)
		got, err := a.GetGuestExecution(ctx, initial.ID)
		require.NoError(t, err)
		require.Equal(t, int64(2), got.Version)
	})
}
func TestGuestRepositoryRejectsCorruptionAndUnknownVersions(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		ctx := context.Background()
		r := open()
		original := executionFixture("corrupt")
		for _, tc := range []struct {
			raw  string
			want error
		}{
			{`{broken`, ErrCorruptRecord}, {`null`, ErrCorruptRecord}, {`{"id":"corrupt","version":1,"status":"queued"}`, ErrUnsupportedVersion},
			{`{"record_format":99,"id":"corrupt","version":1,"status":"queued"}`, ErrUnsupportedVersion},
			{`{"record_format":1,"id":"other","version":1,"status":"queued"}`, ErrCorruptRecord},
			{`{"record_format":1,"id":"corrupt","version":0,"status":"queued"}`, ErrCorruptRecord},
		} {
			// PostgreSQL JSONB rejects invalid syntax before the domain codec can read it.
			if _, postgres := r.records.(*postgresGuestRecords); postgres && !json.Valid([]byte(tc.raw)) {
				continue
			}
			injectGuestRecordForTest(t, r.records, guestExecutionKind, original.ID, []byte(tc.raw))
			_, err := r.GetGuestExecution(ctx, original.ID)
			require.ErrorIs(t, err, tc.want)
			ok, err := r.CompareAndSwapGuestExecution(ctx, original, 1)
			require.ErrorIs(t, err, tc.want)
			require.False(t, ok)
			require.ErrorIs(t, r.SaveGuestExecution(ctx, original), tc.want)
			_, _, err = r.LatestGuestAcceptance(ctx, "agent")
			require.ErrorIs(t, err, tc.want)
		}
	})
}
func TestGuestRepositoryAcceptanceAndTerminalRedaction(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		ctx := context.Background()
		r := open()
		now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		x := executionFixture("terminal")
		x.Status = controlplane.GuestExecutionCompleted
		x.ResultClass = controlplane.GuestExecutionResultClassExit
		x.EndedAt = now
		x.Result = controlplane.GuestExecutionResult{ExitCode: 7, Stdout: "sensitive output"}
		require.NoError(t, r.SaveGuestExecution(ctx, x))
		file := controlplane.GuestFileOperation{ID: "file", Version: 1, Topology: "lab", AgentID: "agent", Status: controlplane.GuestExecutionCompleted, EndedAt: now.Add(time.Minute)}
		require.NoError(t, r.SaveGuestFileOperation(ctx, file))
		ea, fa, err := r.LatestGuestAcceptance(ctx, "agent")
		require.NoError(t, err)
		require.Equal(t, now, ea)
		require.Equal(t, file.EndedAt, fa)
		changed := x
		changed.Result.ExitCode = 0
		ok, err := r.CompareAndSwapGuestExecution(ctx, changed, 1)
		require.ErrorIs(t, err, ErrConflict)
		require.False(t, ok)
		x.Request = controlplane.GuestExecutionRequest{}
		x.Result.Stdout = ""
		ok, err = r.CompareAndSwapGuestExecution(ctx, x, 1)
		require.NoError(t, err)
		require.True(t, ok)
		x.Version = 2
		x.Result.Stdout = "rehydrated"
		ok, err = r.CompareAndSwapGuestExecution(ctx, x, 2)
		require.ErrorIs(t, err, ErrConflict)
		require.False(t, ok)
		file.Status = controlplane.GuestExecutionRunning
		ok, err = r.CompareAndSwapGuestFileOperation(ctx, file, 1)
		require.ErrorIs(t, err, ErrConflict)
		require.False(t, ok)
		cancel, cancelFunc := context.WithCancel(ctx)
		cancelFunc()
		require.ErrorIs(t, r.SaveGuestExecution(cancel, executionFixture("cancelled")), context.Canceled)
		_, err = r.GetGuestExecution(ctx, "cancelled")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

type brokenGuestList struct{ guestRecords }

func (b brokenGuestList) list(context.Context, guestKind) ([]guestRawRecord, error) {
	return nil, errors.New("list unavailable")
}
func TestGuestAcceptanceDoesNotHideListFailure(t *testing.T) {
	r := &GuestRepository{records: brokenGuestList{}}
	a, b, err := r.LatestGuestAcceptance(context.Background(), "agent")
	require.ErrorContains(t, err, "list unavailable")
	require.True(t, a.IsZero())
	require.True(t, b.IsZero())
}

func TestGuestDecodeErrorsDoNotExposePayloads(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		r := open()
		ctx := context.Background()
		raw := []byte(`{"record_format":1,"id":"bad","version":1,"status":"queued","created_at":"PRIVATE_TIMESTAMP_SENTINEL"}`)
		injectGuestRecordForTest(t, r.records, guestExecutionKind, "bad", raw)
		_, err := r.GetGuestExecution(ctx, "bad")
		require.ErrorIs(t, err, ErrCorruptRecord)
		require.NotContains(t, err.Error(), "PRIVATE_TIMESTAMP_SENTINEL")
		var cause *time.ParseError
		require.ErrorAs(t, err, &cause, "internal diagnostics should retain the typed cause")
	})
}
func TestGuestFileRejectsCompletionWithCancellationEvidence(t *testing.T) {
	guestRepositoryFactories(t, func(t *testing.T, open func() *GuestRepository) {
		r := open()
		ctx := context.Background()
		x := controlplane.GuestFileOperation{ID: "file", Version: 1, Topology: "lab", Status: controlplane.GuestExecutionRunning}
		require.NoError(t, r.SaveGuestFileOperation(ctx, x))
		x.Status = controlplane.GuestExecutionCompleted
		x.CancelRequested = true
		ok, err := r.CompareAndSwapGuestFileOperation(ctx, x, 1)
		require.Error(t, err)
		require.False(t, ok)
		got, err := r.GetGuestFileOperation(ctx, x.ID)
		require.NoError(t, err)
		require.Equal(t, controlplane.GuestExecutionRunning, got.Status)
		require.False(t, got.CancelRequested)
		x.ID = "invalid-create"
		require.Error(t, r.SaveGuestFileOperation(ctx, x))
	})
}
