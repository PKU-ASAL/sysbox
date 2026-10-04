package api

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

func (s *localAPIStore) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	var out []controlplane.Run
	pattern := filepath.Join(s.runsDir, "*", "runs.jsonl")
	files, err := localRecordPaths(pattern)
	if err != nil {
		return nil, fmt.Errorf("list local run history: %w", err)
	}
	for _, f := range files {
		runs, err := readLocalHistory[controlplane.Run](ctx, f)
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
	return nil, errLocalRunNotFound
}

func (s *localAPIStore) SaveRun(ctx context.Context, run controlplane.Run) error {
	return persistence.NewRunRepository(localRunStorage{store: s}).SaveRun(ctx, run)
}

func (s *localAPIStore) saveRunUnlocked(ctx context.Context, run controlplane.Run) error {
	if err := validatePathSegment(run.Topology, "topology"); err != nil {
		return err
	}
	if err := validatePathSegment(run.ID, "run"); err != nil {
		return err
	}
	run = persistence.DurableRun(run)
	return appendLocalHistory(ctx, filepath.Join(s.runsDir, run.Topology, "runs.jsonl"), run)
}

func (s *localAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(localRunStorage{store: s}).ClaimRun(ctx, runID, agentID, owner, ttl)
}

func (s *localAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(localRunStorage{store: s}).RenewRunLease(ctx, runID, agentID, owner, ttl)
}

func (s *postgresAPIStore) LoadRuns(ctx context.Context) ([]controlplane.Run, error) {
	conn, err := s.connect(ctx)
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
	normalizeRunProductFields(&run)
	return &run, nil
}

func (s *postgresAPIStore) SaveRun(ctx context.Context, run controlplane.Run) error {
	return persistence.NewRunRepository(postgresRunStorage{store: s}).SaveRun(ctx, run)
}

func (s *postgresAPIStore) ClaimRun(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(postgresRunStorage{store: s}).ClaimRun(ctx, runID, agentID, owner, ttl)
}

func (s *postgresAPIStore) RenewRunLease(ctx context.Context, runID, agentID, owner string, ttl time.Duration) (*controlplane.Run, bool, error) {
	return persistence.NewRunRepository(postgresRunStorage{store: s}).RenewRunLease(ctx, runID, agentID, owner, ttl)
}
