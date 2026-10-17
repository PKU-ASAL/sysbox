package api

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/oslab/sysbox/pkg/persistence"
)

func gcReplay(prior persistence.GCReservation, err error, request persistence.GCReservation) (bool, error) {
	if errors.Is(err, persistence.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if prior.ID != request.ID || prior.AgentID != request.AgentID || prior.Candidate != request.Candidate || request.ReceiptHash != "" {
		return false, persistence.ErrConflict
	}
	return true, nil
}
func checkLocalGCActivity(ctx context.Context, store *localAPIStore, repo *persistence.Repository, topology string, request persistence.GCReservation) error {
	prior, err := repo.LoadGCReservation(ctx, topology, request.ID)
	replay, err := gcReplay(prior, err, request)
	if err != nil || replay {
		return err
	}
	return checkLocalDeleteActivity(ctx, store, topology, request.Candidate.TopologyUID)
}
func checkSQLiteGCActivity(ctx context.Context, conn *sql.Conn, raw []byte, topology string, request persistence.GCReservation) error {
	prior, err := persistence.ReadGCReservation(raw, request.ID)
	replay, err := gcReplay(prior, err, request)
	if err != nil || replay {
		return err
	}
	return checkSQLiteDeleteActivity(ctx, conn, topology, request.Candidate.TopologyUID)
}
func checkPostgresGCActivity(ctx context.Context, tx pgx.Tx, raw []byte, topology string, request persistence.GCReservation) error {
	prior, err := persistence.ReadGCReservation(raw, request.ID)
	replay, err := gcReplay(prior, err, request)
	if err != nil || replay {
		return err
	}
	return checkPostgresDeleteActivity(ctx, tx, topology, request.Candidate.TopologyUID)
}
