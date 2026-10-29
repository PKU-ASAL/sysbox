package hostexec

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestLocalPreparationFailurePrecedesProviderEffects(t *testing.T) {
	for _, operation := range []string{"apply", "destroy", "reset"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			failure := errors.New("local authority unavailable")
			var bridge *LocalBridge
			bridge = NewLocalBridge(LocalOptions{Topology: "lab", StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, PrepareExecution: func(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
				calls++
				probe, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
				unlock, err := bridge.LockTopology(probe, "lab")
				if err == nil {
					unlock()
				}
				require.Error(t, err, "preparation must hold the topology execution lock")
				return ctx, nil, failure
			}})
			run := &controlplane.Run{ID: "test", Topology: "lab", Op: operation}
			NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
			require.Equal(t, 1, calls)
			require.Equal(t, controlplane.RunFailed, run.Status)
			require.Contains(t, run.Err, failure.Error())
			unlock, err := bridge.LockTopology(context.Background(), "lab")
			require.NoError(t, err)
			unlock()
		})
	}
}
