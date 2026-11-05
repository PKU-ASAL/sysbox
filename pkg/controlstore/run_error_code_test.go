package controlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestRunErrorCodeSurvivesSQLiteAndLocalStorage(t *testing.T) {
	for _, backend := range []string{"local", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store Store
			if backend == "local" {
				store = &Local{RunsDir: t.TempDir()}
			} else {
				store = &SQLite{DbPath: t.TempDir() + "/runs.db"}
			}
			run := controlplane.Run{ID: "failed", Topology: "lab", Op: "apply", Operation: "apply", Status: controlplane.RunQueued, QueuedAt: time.Now().UTC()}
			run.MarkFinished(errors.New("provider failed"), time.Now().UTC())
			run.ErrorCode = "observation_unknown"
			require.NoError(t, store.SaveRun(context.Background(), run))
			got, err := store.GetRun(context.Background(), run.ID)
			require.NoError(t, err)
			require.Equal(t, run.ErrorCode, got.ErrorCode)
		})
	}
}
