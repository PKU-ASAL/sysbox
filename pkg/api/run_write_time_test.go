package api

import (
	"context"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
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
	eachRunLeaseStore(t, func(t *testing.T, store controlstore.Store) {
		var storage persistence.RunStorage
		switch s := store.(type) {
		case *controlstore.Local:
			storage = controlstore.LocalRunStorage{Store: s}
		case *controlstore.SQLite:
			storage = controlstore.SqliteRunStorage{Store: s}
		case *controlstore.Postgres:
			storage = controlstore.PostgresRunStorage{Store: s}
		default:
			t.Fatal("unknown backend")
		}
		for _, operation := range []string{"renew"} {
			t.Run(operation, func(t *testing.T) {
				run := controlplane.Run{ID: operation, Topology: operation, Operation: "apply", Status: controlplane.RunRunning, AgentID: "agent", LeaseOwner: "worker", Attempt: 1, LeaseUntil: time.Now().Add(time.Second)}
				require.NoError(t, store.SaveRun(context.Background(), run))
				repo := persistence.NewRunRepository(delayedLeaseWrite{storage})
				_, ok, err := repo.RenewRunLease(context.Background(), run.ID, "agent", "worker", time.Hour)
				require.ErrorIs(t, err, persistence.ErrConflict)
				require.False(t, ok)

				stored, err := store.GetRun(context.Background(), run.ID)
				require.NoError(t, err)
				require.Equal(t, controlplane.RunRunning, stored.Status)
				require.WithinDuration(t, run.LeaseUntil, stored.LeaseUntil, time.Microsecond)
			})
		}
	})
}
