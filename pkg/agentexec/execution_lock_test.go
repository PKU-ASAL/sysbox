package agentexec

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestLocalExecutionWaitCanceledBeforeWorkspaceAccess(t *testing.T) {
	for _, operation := range []string{"apply", "reset", "destroy", "pause"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			opts := LocalOptions{Topology: "lab", StatePath: filepath.Join(root, "state.json"), RunsDir: root, ConfigFile: "missing.hcl", Log: io.Discard}
			owner := NewLocalBridge(opts)
			unlock, err := owner.LockTopology(context.Background(), "lab")
			require.NoError(t, err)
			defer unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			executor := NewExecutorWithBridge(NewLocalBridge(opts))
			if operation == "pause" {
				result := executor.ExecuteNodeOperation(ctx, controlplane.NodeOperation{Topology: "lab", Operation: operation})
				require.Contains(t, result.Err, context.DeadlineExceeded.Error())
				return
			}
			run := &controlplane.Run{ID: "competing", Topology: "lab", Op: operation}
			executor.ExecuteContext(ctx, run)
			require.Contains(t, run.Err, context.DeadlineExceeded.Error())
			require.NotContains(t, run.Err, "missing.hcl", "must acquire execution before reading workspace")
		})
	}
}
