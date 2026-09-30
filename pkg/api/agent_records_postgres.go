package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

type postgresAgentStorage struct{ store *postgresAPIStore }

const postgresAgentColumns = "id, status, disabled, quarantined, protocol, secret_hash, data::text"

func scanPostgresAgent(row runScanner) (*controlplane.Agent, error) {
	var id, status, protocol, secretHash string
	var disabled, quarantined bool
	var raw []byte
	err := row.Scan(&id, &status, &disabled, &quarantined, &protocol, &secretHash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	agent, err := persistence.DecodeAgentRecord(raw)
	if err != nil {
		return nil, err
	}
	if agent.ID != id || agent.Status != status || agent.Disabled != disabled || agent.Quarantined != quarantined || agent.Protocol != protocol || agent.SecretHash != secretHash {
		return nil, persistence.ErrCorruptRecord
	}
	return agent, nil
}
func (b postgresAgentStorage) ReadAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	pool, err := b.store.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	return scanPostgresAgent(pool.QueryRow(ctx, "SELECT "+postgresAgentColumns+" FROM sysbox_agents WHERE id=$1", id))
}
func (b postgresAgentStorage) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	pool, err := b.store.poolFor(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, "SELECT "+postgresAgentColumns+" FROM sysbox_agents")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Agent
	for rows.Next() {
		agent, err := scanPostgresAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *agent)
	}
	return out, rows.Err()
}
func (b postgresAgentStorage) UpdateAgent(ctx context.Context, id string, transform func(*controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error) {
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
	current, err := scanPostgresAgent(tx.QueryRow(ctx, "SELECT "+postgresAgentColumns+" FROM sysbox_agents WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		raw, err := persistence.EncodeAgentRecord(*next)
		if err != nil {
			return nil, err
		}
		if current == nil {
			tag, err := tx.Exec(ctx, "INSERT INTO sysbox_agents (id,status,disabled,quarantined,protocol,secret_hash,data,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,now()) ON CONFLICT(id) DO NOTHING", id, next.Status, next.Disabled, next.Quarantined, next.Protocol, next.SecretHash, string(raw))
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() != 1 {
				return nil, persistence.ErrConflict
			}
		} else {
			_, err = tx.Exec(ctx, "UPDATE sysbox_agents SET status=$2,disabled=$3,quarantined=$4,protocol=$5,secret_hash=$6,data=$7::jsonb,updated_at=now() WHERE id=$1", id, next.Status, next.Disabled, next.Quarantined, next.Protocol, next.SecretHash, string(raw))
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
