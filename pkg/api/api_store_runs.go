package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
)

func (s *localAPIStore) LoadRuns(_ context.Context) ([]controlplane.Run, error) {
	var out []controlplane.Run
	pattern := filepath.Join(s.runsDir, "*", "runs.jsonl")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var r controlplane.Run
			if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
				out = append(out, r)
			}
		}
		fh.Close()
	}
	return out, nil
}

func (s *localAPIStore) GetRun(ctx context.Context, id string) (*controlplane.Run, error) {
	runs, err := s.LoadRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range latestRunsByID(runs) {
		if run.ID == id {
			normalizeRunProductFields(&run)
			return &run, nil
		}
	}
	return nil, fmt.Errorf("run not found")
}

func (s *localAPIStore) SaveRun(_ context.Context, run controlplane.Run) error {
	normalizeRunProductFields(&run)
	dir := filepath.Join(s.runsDir, run.Topology)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "runs.jsonl")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	return json.NewEncoder(fh).Encode(run)
}

func (s *localAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if !runLeasable(*run, agentID, time.Now().UTC()) {
		return run, false, nil
	}
	now := time.Now().UTC()
	run.MarkRunning(owner, ttl, now)
	if err := s.SaveRun(ctx, *run); err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (s *localAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, false, err
	}
	if !run.CanRenewLease(agentID, owner) {
		return run, false, nil
	}
	run.LeaseUntil = time.Now().UTC().Add(ttl)
	if err := s.SaveRun(ctx, *run); err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (s *postgresAPIStore) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT data::text FROM sysbox_runs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres load runs: %w", err)
	}
	defer rows.Close()
	var out []controlplane.Run
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var run controlplane.Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, fmt.Errorf("decode run: %w", err)
		}
		normalizeRunProductFields(&run)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) GetRun(ctx context.Context, id string) (*controlplane.Run, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get run: %w", err)
	}
	var run controlplane.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}
	normalizeRunProductFields(&run)
	return &run, nil
}

func (s *postgresAPIStore) SaveRun(ctx context.Context, run controlplane.Run) error {
	normalizeRunProductFields(&run)
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_runs (topology, id, status, agent_id, lease_owner, lease_until, attempt, data, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, now())
ON CONFLICT (id) DO UPDATE SET topology=EXCLUDED.topology, status=EXCLUDED.status, agent_id=EXCLUDED.agent_id, lease_owner=EXCLUDED.lease_owner, lease_until=EXCLUDED.lease_until, attempt=EXCLUDED.attempt, data=EXCLUDED.data, updated_at=now()`,
		run.Topology, run.ID, string(run.Status), run.AgentID, run.LeaseOwner, nullableTime(run.LeaseUntil), run.Attempt, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save run: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var raw []byte
	var status string
	var leaseUntil *time.Time
	err = tx.QueryRow(ctx, `
SELECT data::text, status, lease_until
FROM sysbox_runs
WHERE id=$1
FOR UPDATE`, runID).Scan(&raw, &status, &leaseUntil)
	if err == pgx.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres load run lease row: %w", err)
	}
	var run controlplane.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, err
	}
	run.Status = controlplane.RunStatus(status)
	if leaseUntil != nil {
		run.LeaseUntil = *leaseUntil
	}
	now := time.Now().UTC()
	if !runLeasable(run, agentID, now) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	run.MarkRunning(owner, ttl, now)
	nextRaw, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sysbox_runs
SET status=$3, lease_owner=$4, lease_until=$5, attempt=$6, data=$7::jsonb, updated_at=now()
WHERE id=$1
  AND agent_id=$2
  AND status='assigned'
  AND (lease_until IS NULL OR lease_until <= $8)`,
		runID, agentID, string(run.Status), run.LeaseOwner, run.LeaseUntil, run.Attempt, string(nextRaw), now)
	if err != nil {
		return nil, false, fmt.Errorf("postgres claim run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}

func (s *postgresAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var raw []byte
	err = tx.QueryRow(ctx, `
SELECT data::text
FROM sysbox_runs
WHERE id=$1
FOR UPDATE`, runID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, false, fmt.Errorf("run not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres load run renew row: %w", err)
	}
	var run controlplane.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, false, err
	}
	if !run.CanRenewLease(agentID, owner) {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	run.LeaseUntil = time.Now().UTC().Add(ttl)
	nextRaw, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE sysbox_runs
SET lease_until=$4, data=$5::jsonb, updated_at=now()
WHERE id=$1 AND agent_id=$2 AND lease_owner=$3 AND status='running'`,
		runID, agentID, owner, run.LeaseUntil, string(nextRaw))
	if err != nil {
		return nil, false, fmt.Errorf("postgres renew run lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, err
		}
		return &run, false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}
