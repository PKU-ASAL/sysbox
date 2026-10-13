package hostsupervisor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestReturnedRunWithoutActionsCanFailButNeverProveCleanup(t *testing.T) {
	for _, operation := range []string{"apply", "reset", "destroy"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now().UTC()
			run := controlplane.Run{ID: "run", Topology: "lab", Op: operation, Status: controlplane.RunFailed, QueuedAt: now, EndedAt: now, Err: "invalid config", Recoverable: true, Inputs: map[string]string{"secret": "private"}}
			var stopped *quiescentRunFailure
			err := FinishReturnedRunWithoutActions(context.Background(), &RunChildren{}, &run, nil)
			require.ErrorAs(t, err, &stopped)
			var result controlplane.Run
			require.NoError(t, json.Unmarshal(stopped.evidence, &result))
			require.Equal(t, controlplane.RunFailed, result.Status)
			require.Nil(t, result.Cleanup)
			require.Nil(t, result.Inputs)
			require.False(t, result.Recoverable)
			for _, actions := range [][]runtime.ActionRecord{
				{{RunID: "run", Status: runtime.ActionSucceeded}},
				{{RunID: "other", Status: runtime.ActionUncertain}},
			} {
				require.NotErrorAs(t, FinishReturnedRunWithoutActions(context.Background(), &RunChildren{}, &run, actions), &stopped)
			}
			require.NotErrorAs(t, FinishReturnedRunWithoutActions(context.Background(), &RunChildren{unknownAdmission: true}, &run, nil), &stopped)
			run.ParentID = "original-reset"
			require.NotErrorAs(t, FinishReturnedRunWithoutActions(context.Background(), &RunChildren{}, &run, nil), &stopped)
		})
	}
}
