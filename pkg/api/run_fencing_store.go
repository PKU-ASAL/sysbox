package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"sync"
	"time"
)

// Ownership includes attempt and lease deadline: an expiry scan must not fence
// a concurrent successful renewal, and an old process must not complete a new claim.
func sameRunOwnership(a, b controlplane.Run) bool {
	return a.ID == b.ID && a.AgentID == b.AgentID && a.Status == b.Status && a.LeaseOwner == b.LeaseOwner && a.Attempt == b.Attempt && a.LeaseUntil.Equal(b.LeaseUntil)
}

// local stores are not advertised as durable multi-process execution backends.
var localRunTransitionMu sync.Mutex

func (s *localAPIStore) CompareAndSwapRun(ctx context.Context, expected, next controlplane.Run, requireLive bool) (bool, error) {
	localRunTransitionMu.Lock()
	defer localRunTransitionMu.Unlock()
	current, err := s.GetRun(ctx, expected.ID)
	if err != nil {
		return false, err
	}
	if !sameRunOwnership(*current, expected) || (requireLive && !current.LeaseUntil.After(time.Now())) {
		return false, nil
	}
	return true, s.SaveRun(ctx, next)
}
func (s *sqliteAPIStore) CompareAndSwapRun(ctx context.Context, expected, next controlplane.Run, requireLive bool) (bool, error) {
	db, err := s.open()
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id,topology,operation,op,status,error,parent_id,revision,target,agent_id,recoverable,unsafe_state,operation_key,request_fingerprint,protocol,lease_owner,lease_until,attempt,queued_at,assigned_at,started_at,ended_at,deadline_at,snapshot_path,completion_projection FROM sysbox_runs WHERE id=? ORDER BY rowid DESC LIMIT 1`, expected.ID)
	current, err := scanSQLiteRun(row)
	if err != nil {
		return false, err
	}
	if !sameRunOwnership(*current, expected) || (requireLive && !current.LeaseUntil.After(time.Now())) {
		return false, nil
	}
	if next.ID != expected.ID {
		return false, fmt.Errorf("run identity mismatch")
	}
	written, err := insertSQLiteRunChecked(ctx, tx, next, requireLive)
	if err != nil || !written {
		return false, err
	}
	return true, tx.Commit()
}
func (s *postgresAPIStore) CompareAndSwapRun(ctx context.Context, expected, next controlplane.Run, requireLive bool) (bool, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT data::text FROM sysbox_runs WHERE id=$1 FOR UPDATE`, expected.ID).Scan(&raw); err != nil {
		return false, err
	}
	var current controlplane.Run
	if err = json.Unmarshal(raw, &current); err != nil {
		return false, err
	}
	if !sameRunOwnership(current, expected) || (requireLive && !current.LeaseUntil.After(time.Now())) {
		return false, nil
	}
	if next.ID != expected.ID {
		return false, fmt.Errorf("run identity mismatch")
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE sysbox_runs SET status=$2,lease_owner=$3,lease_until=$4,attempt=$5,data=$6::jsonb,updated_at=now() WHERE id=$1 AND ($7=false OR lease_until>clock_timestamp())`, next.ID, next.Status, next.LeaseOwner, nullableTime(next.LeaseUntil), next.Attempt, string(raw), requireLive)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	return true, tx.Commit(ctx)
}

func completionProjectionJSON(p *controlplane.Projection) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}
