package hostexec

import (
	"context"
	"errors"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// runGuestExec runs argv in a guest node identified by logical name, via the
// guest-exec capability. It is the single "run a command in a node" primitive,
// shared by the guest-execution API (executeGuestOperation) and check evaluation
// (evaluateChecks) — do not add another exec path.
//
// ctx carries the caller's timeout/cancellation; the caller applies the budget,
// so this stays a pure lookup-and-exec. It returns the raw result; the error is
// wrapped so callers can distinguish resolution failures (node not found,
// capability unavailable) from a failed exec.
func RunGuestExec(ctx context.Context, st *state.State, node string, argv []string, env map[string]string, workingDir string) (substrate.ExecResult, error) {
	return RunGuestExecCapture(ctx, st, node, argv, env, workingDir, false)
}

func RunGuestExecCapture(ctx context.Context, st *state.State, node string, argv []string, env map[string]string, workingDir string, capture bool) (substrate.ExecResult, error) {
	if len(argv) == 0 {
		return substrate.ExecResult{}, fmt.Errorf("exec program is required")
	}
	request := substrate.ExecRequest{Program: argv[0], Args: argv[1:], Environment: env, WorkingDir: workingDir, Shell: substrate.ShellNone, OutputLimitBytes: controlplane.GuestOutputLimit, CaptureViaFile: capture}
	if err := request.Validate(); err != nil {
		return substrate.ExecResult{}, err
	}
	res := st.FindResource(address.Resource("sysbox_node", node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", node))
	}
	if res == nil {
		return substrate.ExecResult{}, fmt.Errorf("%w: %s", controlplane.ErrGuestNodeNotFound, node)
	}
	_, err := driver.DefaultRegistry.RequireGuestExec(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", controlplane.ErrGuestExecUnsupported)
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", controlplane.ErrGuestExecUnsupported)
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", controlplane.ErrGuestExecUnsupported)
	}
	handle, err := res.ReconstructHandle(codec)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("reconstruct provider handle: %w", err)
	}
	conn, err := nodeDriver.Connection(handle, nil)
	if err != nil {
		if errors.Is(err, substrate.ErrNotSupported) {
			return substrate.ExecResult{}, fmt.Errorf("%w", controlplane.ErrGuestExecUnsupported)
		}
		return substrate.ExecResult{}, fmt.Errorf("connect to guest: %w", err)
	}
	if conn == nil {
		return substrate.ExecResult{}, fmt.Errorf("%w", controlplane.ErrGuestExecUnsupported)
	}
	result, err := conn.Exec(ctx, request, nil, nil)
	if err == nil && !result.ExitCodeKnown {
		err = fmt.Errorf("guest execution did not report an exit status")
	}
	return result, err
}
