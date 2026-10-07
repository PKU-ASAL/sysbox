package persistence

import (
	"context"
	"database/sql"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewFilesystemExecutionGrantRepository(root string) *ExecutionGrantRepository {
	return newExecutionGrantRepository(&fileGuestRecords{root: root})
}

func FilesystemExecutionGrantStorage(root string) ExecutionGrantStorage {
	return executionGrantRecords{records: &fileGuestRecords{root: root}}
}

// SQL constructors borrow their pools and initialize an independent versioned
// authority table. They never migrate old records or reset existing epochs.
func NewSQLiteExecutionGrantRepository(ctx context.Context, db *sql.DB) (*ExecutionGrantRepository, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sysbox_execution_authority(id TEXT PRIMARY KEY,data BLOB NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return newExecutionGrantRepository(&sqliteGuestRecords{db: db}), nil
}
func NewPostgresExecutionGrantRepository(ctx context.Context, pool *pgxpool.Pool) (*ExecutionGrantRepository, error) {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS sysbox_execution_authority(id TEXT PRIMARY KEY,data JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		return nil, err
	}
	return newExecutionGrantRepository(&postgresGuestRecords{pool: pool}), nil
}
