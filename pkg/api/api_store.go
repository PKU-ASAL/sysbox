package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oslab/sysbox/pkg/runtime"
)

type apiStore interface {
	LoadRuns(ctx context.Context) ([]Run, error)
	SaveRun(ctx context.Context, run Run) error
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error
	LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error)
	SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error
	LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error)
}

type localAPIStore struct {
	runsDir string
}

func newAPIStore(runsDir, backendURL string) apiStore {
	if strings.HasPrefix(backendURL, "postgres://") || strings.HasPrefix(backendURL, "postgresql://") {
		return &postgresAPIStore{dsn: backendURL}
	}
	return &localAPIStore{runsDir: runsDir}
}

func (s *localAPIStore) LoadRuns(_ context.Context) ([]Run, error) {
	var out []Run
	pattern := filepath.Join(s.runsDir, "*", "runs.jsonl")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			var r Run
			if err := json.Unmarshal(sc.Bytes(), &r); err == nil {
				out = append(out, r)
			}
		}
		fh.Close()
	}
	return out, nil
}

func (s *localAPIStore) SaveRun(_ context.Context, run Run) error {
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

func (s *localAPIStore) SaveCheckpoint(_ context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error {
	return runtime.WriteCheckpoint(s.checkpointFile(topology, runID), checkpoint)
}

func (s *localAPIStore) LoadCheckpoint(_ context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	raw, err := os.ReadFile(s.checkpointFile(topology, runID))
	if err != nil {
		return nil, fmt.Errorf("checkpoint not found")
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return &cp, nil
}

func (s *localAPIStore) SaveHealth(_ context.Context, topology string, snap HealthSnapshot) error {
	path := filepath.Join(s.runsDir, topology, "health.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (s *localAPIStore) LoadHealth(_ context.Context, topology string) (*HealthSnapshot, error) {
	raw, err := os.ReadFile(filepath.Join(s.runsDir, topology, "health.json"))
	if err != nil {
		return nil, fmt.Errorf("health snapshot not found")
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode health snapshot: %w", err)
	}
	return &snap, nil
}

func (s *localAPIStore) checkpointFile(topology, runID string) string {
	return filepath.Join(s.runsDir, topology, "runs", runID+".checkpoint.json")
}

type postgresAPIStore struct {
	dsn string
}

func (s *postgresAPIStore) connect(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, dsnWithoutSysboxQuery(s.dsn))
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	if err := s.ensureSchema(ctx, conn); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return conn, nil
}

func (s *postgresAPIStore) ensureSchema(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sysbox_runs (
  topology TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_checkpoints (
  topology TEXT NOT NULL,
  run_id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_health (
  topology TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	if err != nil {
		return fmt.Errorf("postgres ensure api tables: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadRuns(ctx context.Context) ([]Run, error) {
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
	var out []Run
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(raw, &run); err != nil {
			return nil, fmt.Errorf("decode run: %w", err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) SaveRun(ctx context.Context, run Run) error {
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
INSERT INTO sysbox_runs (topology, id, data, updated_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (id) DO UPDATE SET topology=EXCLUDED.topology, data=EXCLUDED.data, updated_at=now()`,
		run.Topology, run.ID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save run: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_checkpoints (topology, run_id, data, updated_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (run_id) DO UPDATE SET topology=EXCLUDED.topology, data=EXCLUDED.data, updated_at=now()`,
		topology, runID, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save checkpoint: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_checkpoints WHERE topology=$1 AND run_id=$2`, topology, runID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("checkpoint not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres load checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}
	return &cp, nil
}

func (s *postgresAPIStore) SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
INSERT INTO sysbox_health (topology, data, updated_at)
VALUES ($1, $2::jsonb, now())
ON CONFLICT (topology) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`,
		topology, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save health: %w", err)
	}
	return nil
}

func (s *postgresAPIStore) LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, `SELECT data::text FROM sysbox_health WHERE topology=$1`, topology).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("health snapshot not found")
	}
	if err != nil {
		return nil, fmt.Errorf("postgres load health: %w", err)
	}
	var snap HealthSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("decode health snapshot: %w", err)
	}
	return &snap, nil
}

func dsnWithoutSysboxQuery(raw string) string {
	out := raw
	if idx := strings.Index(out, "?"); idx >= 0 {
		out = out[:idx]
	}
	return out
}

func markInterruptedRuns(runs []Run) []Run {
	for i := range runs {
		if runs[i].Status == RunRunning {
			runs[i].Status = RunFailed
			runs[i].Err = "server restarted before run completion"
			runs[i].Recoverable = true
			if runs[i].EndedAt.IsZero() {
				runs[i].EndedAt = time.Now().UTC()
			}
		}
	}
	return runs
}
