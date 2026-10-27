package guesttask

import (
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExecutionCommandRejectsUnsupportedCaptureOptions(t *testing.T) {
	for _, req := range []controlplane.GuestExecutionRequest{
		{Argv: []string{"true"}, CaptureViaFile: true},
		{Argv: []string{"true"}, OutputLimitBytes: 8 << 20},
	} {
		_, err := FromExecutionCommand(controlplane.AgentCommand{Type: "guest_execution", ActionID: "task-1", Execution: &controlplane.GuestExecution{}, ExecutionRequest: req})
		require.ErrorContains(t, err, "do not support")
	}
}
