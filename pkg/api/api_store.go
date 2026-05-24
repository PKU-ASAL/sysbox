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

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

type apiStore interface {
	LoadRuns(ctx context.Context) ([]Run, error)
	GetRun(ctx context.Context, id string) (*Run, error)
	SaveRun(ctx context.Context, run Run) error
	SaveCheckpoint(ctx context.Context, topology, runID string, checkpoint runtime.OperationCheckpoint) error
	LoadCheckpoint(ctx context.Context, topology, runID string) (*runtime.OperationCheckpoint, error)
	SaveHealth(ctx context.Context, topology string, snap HealthSnapshot) error
	LoadHealth(ctx context.Context, topology string) (*HealthSnapshot, error)
	SaveRevision(ctx context.Context, rev controlplane.Revision) error
	ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error)
	GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error)
	SavePlan(ctx context.Context, plan controlplane.Plan) error
	ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error)
	GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error)
	SavePolicy(ctx context.Context, policy controlplane.Policy) error
	ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error)
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

func (s *localAPIStore) GetRun(ctx context.Context, id string) (*Run, error) {
	runs, err := s.LoadRuns(ctx)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if runs[i].ID == id {
			normalizeRunProductFields(&runs[i])
			return &runs[i], nil
		}
	}
	return nil, fmt.Errorf("run not found")
}

func (s *localAPIStore) SaveRun(_ context.Context, run Run) error {
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

func (s *localAPIStore) SaveRevision(_ context.Context, rev controlplane.Revision) error {
	return writeLocalObject(filepath.Join(s.runsDir, rev.Workspace, "revisions", rev.ID+".json"), rev)
}

func (s *localAPIStore) ListRevisions(_ context.Context, workspace string) ([]controlplane.Revision, error) {
	return readLocalObjects[controlplane.Revision](filepath.Join(s.runsDir, workspace, "revisions", "*.json"))
}

func (s *localAPIStore) GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error) {
	items, err := s.ListRevisions(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == revisionID {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("revision not found")
}

func (s *localAPIStore) SavePlan(_ context.Context, plan controlplane.Plan) error {
	return writeLocalObject(filepath.Join(s.runsDir, plan.Workspace, "plans", plan.ID+".json"), plan)
}

func (s *localAPIStore) ListPlans(_ context.Context, workspace string) ([]controlplane.Plan, error) {
	return readLocalObjects[controlplane.Plan](filepath.Join(s.runsDir, workspace, "plans", "*.json"))
}

func (s *localAPIStore) GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error) {
	items, err := s.ListPlans(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == planID {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("plan not found")
}

func (s *localAPIStore) SavePolicy(_ context.Context, policy controlplane.Policy) error {
	workspace := policy.Workspace
	if workspace == "" {
		workspace = "_project"
	}
	return writeLocalObject(filepath.Join(s.runsDir, workspace, "policies", policy.ID+".json"), policy)
}

func (s *localAPIStore) ListPolicies(_ context.Context, workspace string) ([]controlplane.Policy, error) {
	if workspace == "" {
		workspace = "_project"
	}
	return readLocalObjects[controlplane.Policy](filepath.Join(s.runsDir, workspace, "policies", "*.json"))
}

func writeLocalObject(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func readLocalObjects[T any](pattern string) ([]T, error) {
	files, _ := filepath.Glob(pattern)
	out := make([]T, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var item T
		if err := json.Unmarshal(raw, &item); err == nil {
			out = append(out, item)
		}
	}
	return out, nil
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
);
CREATE TABLE IF NOT EXISTS sysbox_revisions (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_plans (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sysbox_policies (
  workspace TEXT NOT NULL,
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
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
		normalizeRunProductFields(&run)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *postgresAPIStore) GetRun(ctx context.Context, id string) (*Run, error) {
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
	var run Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("decode run: %w", err)
	}
	normalizeRunProductFields(&run)
	return &run, nil
}

func (s *postgresAPIStore) SaveRun(ctx context.Context, run Run) error {
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

func (s *postgresAPIStore) SaveRevision(ctx context.Context, rev controlplane.Revision) error {
	return s.saveObject(ctx, "sysbox_revisions", rev.Workspace, rev.ID, rev)
}

func (s *postgresAPIStore) ListRevisions(ctx context.Context, workspace string) ([]controlplane.Revision, error) {
	return listPostgresObjects[controlplane.Revision](ctx, s, "sysbox_revisions", workspace)
}

func (s *postgresAPIStore) GetRevision(ctx context.Context, workspace, revisionID string) (*controlplane.Revision, error) {
	return getPostgresObject[controlplane.Revision](ctx, s, "sysbox_revisions", workspace, revisionID)
}

func (s *postgresAPIStore) SavePlan(ctx context.Context, plan controlplane.Plan) error {
	return s.saveObject(ctx, "sysbox_plans", plan.Workspace, plan.ID, plan)
}

func (s *postgresAPIStore) ListPlans(ctx context.Context, workspace string) ([]controlplane.Plan, error) {
	return listPostgresObjects[controlplane.Plan](ctx, s, "sysbox_plans", workspace)
}

func (s *postgresAPIStore) GetPlan(ctx context.Context, workspace, planID string) (*controlplane.Plan, error) {
	return getPostgresObject[controlplane.Plan](ctx, s, "sysbox_plans", workspace, planID)
}

func (s *postgresAPIStore) SavePolicy(ctx context.Context, policy controlplane.Policy) error {
	workspace := policy.Workspace
	if workspace == "" {
		workspace = "_project"
	}
	return s.saveObject(ctx, "sysbox_policies", workspace, policy.ID, policy)
}

func (s *postgresAPIStore) ListPolicies(ctx context.Context, workspace string) ([]controlplane.Policy, error) {
	if workspace == "" {
		workspace = "_project"
	}
	return listPostgresObjects[controlplane.Policy](ctx, s, "sysbox_policies", workspace)
}

func (s *postgresAPIStore) saveObject(ctx context.Context, table, workspace, id string, v any) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (workspace, id, data, created_at)
VALUES ($1, $2, $3::jsonb, now())
ON CONFLICT (id) DO UPDATE SET workspace=EXCLUDED.workspace, data=EXCLUDED.data`, table),
		workspace, id, string(raw))
	if err != nil {
		return fmt.Errorf("postgres save %s: %w", table, err)
	}
	return nil
}

func listPostgresObjects[T any](ctx context.Context, s *postgresAPIStore, table, workspace string) ([]T, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE workspace=$1 ORDER BY created_at DESC`, table), workspace)
	if err != nil {
		return nil, fmt.Errorf("postgres list %s: %w", table, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func getPostgresObject[T any](ctx context.Context, s *postgresAPIStore, table, workspace, id string) (*T, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var raw []byte
	err = conn.QueryRow(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE workspace=$1 AND id=$2`, table), workspace, id).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("object not found")
	}
	if err != nil {
		return nil, err
	}
	var item T
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	return &item, nil
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
