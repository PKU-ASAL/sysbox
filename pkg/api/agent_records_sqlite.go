package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"time"
)

type sqliteAgentStorage struct{ store *sqliteAPIStore }

const sqliteAgentColumns = "id, name, status, disabled, quarantined, reason, auth_secret, secret_hash, protocol, capabilities, labels, version, last_heartbeat, created_at, updated_at"

func scanSQLiteAgent(row runScanner) (*controlplane.Agent, error) {
	var agent controlplane.Agent
	var caps, labels []byte
	var heartbeat, created, updated string
	var disabled, quarantined int
	err := row.Scan(&agent.ID, &agent.Name, &agent.Status, &disabled, &quarantined, &agent.Reason, &agent.AuthSecret, &agent.SecretHash, &agent.Protocol, &caps, &labels, &agent.Version, &heartbeat, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if (disabled != 0 && disabled != 1) || (quarantined != 0 && quarantined != 1) {
		return nil, persistence.ErrCorruptRecord
	}
	agent.Disabled = disabled == 1
	agent.Quarantined = quarantined == 1
	if json.Unmarshal(caps, &agent.Capabilities) != nil || json.Unmarshal(labels, &agent.Labels) != nil {
		return nil, persistence.ErrCorruptRecord
	}
	for _, field := range []struct {
		raw string
		dst *time.Time
	}{{heartbeat, &agent.LastHeartbeat}, {created, &agent.CreatedAt}, {updated, &agent.UpdatedAt}} {
		if field.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, field.raw)
		if err != nil {
			return nil, persistence.ErrCorruptRecord
		}
		*field.dst = parsed
	}
	raw, err := json.Marshal(agent)
	if err != nil {
		return nil, persistence.ErrCorruptRecord
	}
	return persistence.DecodeAgentRecord(raw)
}
func (b sqliteAgentStorage) ReadAgent(ctx context.Context, id string) (*controlplane.Agent, error) {
	db, err := b.store.open()
	if err != nil {
		return nil, err
	}
	return scanSQLiteAgent(db.QueryRowContext(ctx, "SELECT "+sqliteAgentColumns+" FROM sysbox_agents WHERE id=?", id))
}
func (b sqliteAgentStorage) ListAgents(ctx context.Context) ([]controlplane.Agent, error) {
	db, err := b.store.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+sqliteAgentColumns+" FROM sysbox_agents")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []controlplane.Agent
	for rows.Next() {
		agent, err := scanSQLiteAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *agent)
	}
	return out, rows.Err()
}
func (b sqliteAgentStorage) UpdateAgent(ctx context.Context, id string, transform func(*controlplane.Agent) (*controlplane.Agent, error)) (*controlplane.Agent, error) {
	db, err := b.store.open()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
	}()
	current, err := scanSQLiteAgent(conn.QueryRowContext(ctx, "SELECT "+sqliteAgentColumns+" FROM sysbox_agents WHERE id=?", id))
	if err != nil {
		return nil, err
	}
	next, err := transform(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		caps, err := json.Marshal(next.Capabilities)
		if err != nil {
			return nil, err
		}
		labels, err := json.Marshal(next.Labels)
		if err != nil {
			return nil, err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO sysbox_agents ("+sqliteAgentColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,status=excluded.status,disabled=excluded.disabled,quarantined=excluded.quarantined,reason=excluded.reason,auth_secret=excluded.auth_secret,secret_hash=excluded.secret_hash,protocol=excluded.protocol,capabilities=excluded.capabilities,labels=excluded.labels,version=excluded.version,last_heartbeat=excluded.last_heartbeat,created_at=excluded.created_at,updated_at=excluded.updated_at", next.ID, next.Name, next.Status, next.Disabled, next.Quarantined, next.Reason, next.AuthSecret, next.SecretHash, next.Protocol, string(caps), string(labels), next.Version, formatSQLiteTime(next.LastHeartbeat), formatSQLiteTime(next.CreatedAt), formatSQLiteTime(next.UpdatedAt))
		if err != nil {
			return nil, err
		}
		current = next
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true
	return current, nil
}
