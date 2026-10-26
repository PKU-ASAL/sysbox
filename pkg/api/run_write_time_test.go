package api

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type delayedLeaseWrite struct{ persistence.RunStorage }

func (s delayedLeaseWrite) UpdateRun(ctx context.Context, id string, fn func(*controlplane.Run) (*controlplane.Run, error)) (*controlplane.Run, error) {
	return s.RunStorage.UpdateRun(ctx, id, func(current *controlplane.Run) (*controlplane.Run, error) {
		next, err := fn(current)
		if err == nil && next != nil {
			time.Sleep(time.Until(current.LeaseUntil) + 30*time.Millisecond)
		}
		return next, err
	})
}
func TestRunLeaseCheckedAtStorageWrite(t *testing.T) {
	eachRunLeaseStore(t, func(t *testing.T, store apiStore) {
		var storage persistence.RunStorage
		switch s := store.(type) {
		case *localAPIStore:
			storage = localRunStorage{store: s}
		case *sqliteAPIStore:
			storage = sqliteRunStorage{store: s}
		case *postgresAPIStore:
			storage = postgresRunStorage{store: s}
		default:
			t.Fatal("unknown backend")
		}
		for _, operation := range []string{"renew", "complete"} {
			t.Run(operation, func(t *testing.T) {
				run := controlplane.Run{ID: operation, Topology: operation, Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Second)}
				require.NoError(t, store.SaveRun(context.Background(), run))
				repo := persistence.NewRunRepository(delayedLeaseWrite{storage})
				if operation == "renew" {
					_, ok, err := repo.RenewRunLease(context.Background(), run.ID, "agent", "worker", time.Hour)
					require.ErrorIs(t, err, persistence.ErrConflict)
					require.False(t, ok)
				} else {
					report := run
					report.Status = controlplane.RunDone
					_, ok, err := repo.CompleteRun(context.Background(), "agent", run.ID, controlplane.RunCompletion{Run: report})
					require.ErrorIs(t, err, persistence.ErrConflict)
					require.False(t, ok)
				}
				stored, err := store.GetRun(context.Background(), run.ID)
				require.NoError(t, err)
				require.Equal(t, controlplane.RunRunning, stored.Status)
				require.WithinDuration(t, run.LeaseUntil, stored.LeaseUntil, time.Microsecond)
			})
		}
	})
}
