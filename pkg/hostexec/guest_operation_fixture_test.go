package hostexec

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
)

func executeGuestOperation(ctx context.Context, st *state.State, node string, req controlplane.GuestExecutionRequest) controlplane.GuestOperationCompletion {
	ctx, cancel := controlplane.GuestOperationContext(ctx, req)
	defer cancel()

	result, err := RunGuestExecCapture(ctx, st, node, req.Argv, req.Environment, req.WorkingDirectory, req.CaptureViaFile)
	if err != nil {
		class := controlplane.ClassifyGuestExecError(ctx, err)
		switch class {
		case "not_found":
			return controlplane.GuestOperationCompletion{ResultClass: class, Err: "logical node not found"}
		case "unsupported":
			return controlplane.GuestOperationCompletion{ResultClass: class, Err: "guest execution capability unavailable"}
		default:
			// Do not drop what the provider already captured: on a timeout that is
			// the only trace of how far the command got, and the caller has nothing
			// else to act on.
			return controlplane.GuestOperationCompletion{ResultClass: class, Err: class + " guest operation", Result: controlplane.EncodeGuestResult(result)}
		}
	}

	out := controlplane.EncodeGuestResult(result)
	out.ExitCode = result.ExitCode
	return controlplane.GuestOperationCompletion{ResultClass: controlplane.GuestExecutionResultClassExit, Result: out}
}
