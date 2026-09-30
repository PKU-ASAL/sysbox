package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/persistence"
)

type postgresCommandStorage struct{ store *postgresAPIStore }

const postgresCommandColumns = "id, agent_id, status, lease_owner, lease_until, attempt, data::text"

func scanPostgresCommand(row runScanner) (*persistence.CommandRecord, error) {
	var id, agent, status, owner string
	var until *time.Time
	var attempt int
	var raw []byte
	err := row.Scan(&id, &agent, &status, &owner, &until, &attempt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := persistence.DecodeCommandRecord(raw)
	if err != nil {
		return nil, err
	}
	cmd := record.Command
	timestampMatches := until == nil && cmd.LeaseUntil.IsZero() || until != nil && until.Equal(cmd.LeaseUntil.Truncate(time.Microsecond))
	if id != cmd.ID || agent != cmd.AgentID || status != cmd.Status || owner != cmd.LeaseOwner || attempt != cmd.Attempt || !timestampMatches {
		return nil, persistence.ErrCorruptRecord
	}
	return record, nil
}
func (b postgresCommandStorage) ReadCommand(ctx context.Context, id string) (*persistence.CommandRecord, error) {
	pool, err := b.store.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	return scanPostgresCommand(pool.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1", id))
}
func (b postgresCommandStorage) ListCommands(ctx context.Context, agent string) ([]persistence.CommandRecord, error) {
	pool, err := b.store.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + postgresCommandColumns + " FROM sysbox_agent_commands"
	args := []any{}
	if agent != "" {
		query += " WHERE agent_id=$1"
		args = append(args, agent)
	}
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.CommandRecord
	for rows.Next() {
		record, err := scanPostgresCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}
func (b postgresCommandStorage) UpdateCommand(ctx context.Context, id string, transform func(*persistence.CommandRecord) (*persistence.CommandRecord, error)) (*persistence.CommandRecord, error) {
	pool, err := b.store.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	current, err := scanPostgresCommand(tx.QueryRow(ctx, "SELECT "+postgresCommandColumns+" FROM sysbox_agent_commands WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		raw, err := persistence.EncodeCommandRecord(*next)
		if err != nil {
			return nil, err
		}
		cmd := next.Command
		if current == nil {
			tag, err := tx.Exec(ctx, "INSERT INTO sysbox_agent_commands (id,agent_id,status,lease_owner,lease_until,attempt,data,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,now()) ON CONFLICT(id) DO NOTHING", id, cmd.AgentID, cmd.Status, cmd.LeaseOwner, nullableTime(cmd.LeaseUntil), cmd.Attempt, string(raw))
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() != 1 {
				return nil, persistence.ErrConflict
			}
		} else {
			_, err = tx.Exec(ctx, "UPDATE sysbox_agent_commands SET agent_id=$2,status=$3,lease_owner=$4,lease_until=$5,attempt=$6,data=$7::jsonb,updated_at=now() WHERE id=$1", id, cmd.AgentID, cmd.Status, cmd.LeaseOwner, nullableTime(cmd.LeaseUntil), cmd.Attempt, string(raw))
			if err != nil {
				return nil, err
			}
		}
		current = next
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return current, nil
}
