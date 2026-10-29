package controlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

var errLocalRunNotFound = persistence.ErrNotFound

func (s *Local) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	var out []controlplane.Run
	pattern := filepath.Join(s.RunsDir, "*", "runs.jsonl")
	files, err := LocalRecordPaths(pattern)
	if err != nil {
		return nil, fmt.Errorf("list local run history: %w", err)
	}
	for _, f := range files {
		runs, err := ReadLocalHistory[controlplane.Run](ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	dispatches, err := s.loadRunDispatches()
	if err != nil {
		return nil, err
	}
	for _, dispatch := range dispatches {
		out = append(out, dispatch.Run)
	}
	return out, nil
}

func (s *Local) GetRun(ctx context.Context, id string) (*controlplane.Run, error) {
	runs, err := s.LoadRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range LatestRunsByID(runs) {
		if run.ID == id {
			NormalizeRunProductFields(&run)
			return &run, nil
		}
	}
	return nil, errLocalRunNotFound
}

func (s *Local) SaveRun(ctx context.Context, run controlplane.Run) error {
	return persistence.NewRunRepository(LocalRunStorage{Store: s}).SaveRun(ctx, run)
}

func (s *Local) SaveRunUnlocked(ctx context.Context, run controlplane.Run) error {
	if err := ValidatePathSegment(run.Topology, "topology"); err != nil {
		return err
	}
	if err := ValidatePathSegment(run.ID, "run"); err != nil {
		return err
	}
	run = persistence.DurableRun(run)
	return appendLocalHistory(ctx, filepath.Join(s.RunsDir, run.Topology, "runs.jsonl"), run)
}

func (s *Local) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(LocalRunStorage{Store: s}).ClaimRun(ctx, runID, agentID, owner, ttl)
}

func (s *Local) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(LocalRunStorage{Store: s}).RenewRunLease(ctx, runID, agentID, owner, ttl)
}

func (s *Postgres) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	conn, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
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
		NormalizeRunProductFields(&run)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Postgres) GetRun(ctx context.Context, id string) (*controlplane.Run, error) {
	conn, err := s.Connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1`, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, persistence.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres get run: %w", err)
	}
	var run controlplane.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}
	NormalizeRunProductFields(&run)
	return &run, nil
}

func (s *Postgres) SaveRun(ctx context.Context, run controlplane.Run) error {
	return persistence.NewRunRepository(PostgresRunStorage{Store: s}).SaveRun(ctx, run)
}

func (s *Postgres) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(PostgresRunStorage{Store: s}).ClaimRun(ctx, runID, agentID, owner, ttl)
}

func (s *Postgres) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(PostgresRunStorage{Store: s}).RenewRunLease(ctx, runID, agentID, owner, ttl)
}
