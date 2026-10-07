package agentexec

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"os"
	"time"
)

type durableGuestTaskConnection interface {
	substrate.ConnectionStarter
	substrate.ConnectionExecutionObserver
	substrate.ConnectionExecutionCanceler
	substrate.ConnectionExecutionOutput
	substrate.ConnectionExecutionRenewer
	GuestTaskBootID(context.Context) (string, error)
}

func executeGrantedGuestOperation(ctx context.Context, opts Options, st *state.State, cmd controlplane.AgentCommand) guestOperationCompletion {
	result, err := runGrantedGuestTask(ctx, opts, st, cmd)
	if err != nil {
		class := classifyGuestExecError(ctx, err)
		return guestOperationCompletion{Result: encodeGuestResult(result), ResultClass: class, Err: class + " guest operation"}
	}
	out := encodeGuestResult(result)
	out.ExitCode = result.ExitCode
	return guestOperationCompletion{Result: out, ResultClass: controlplane.GuestExecutionResultClassExit}
}

func runGrantedGuestTask(ctx context.Context, opts Options, st *state.State, cmd controlplane.AgentCommand) (substrate.ExecResult, error) {
	if opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return substrate.ExecResult{}, errors.New("guest execution requires an authenticated session and pinned grant issuer key")
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	if len(key) != ed25519.PublicKeySize {
		return substrate.ExecResult{}, controlplane.ErrInvalidExecutionGrant
	}
	task, err := guesttask.FromExecutionCommand(cmd)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	res := st.FindResource(address.Resource("sysbox_node", cmd.Execution.Node))
	if res == nil {
		res = st.FindResource(address.Resource("sysbox_router", cmd.Execution.Node))
	}
	if res == nil {
		return substrate.ExecResult{}, errGuestNodeNotFound
	}
	if res.ResourceUID != cmd.ResourceUID || res.Generation != cmd.Generation {
		return substrate.ExecResult{}, guesttask.ErrConflict
	}
	d, err := driver.DefaultRegistry.RequireNode(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, errGuestExecUnsupported
	}
	codec, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	handle, err := res.ReconstructHandle(codec)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	conn, err := d.Connection(handle, nil)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	client, ok := conn.(durableGuestTaskConnection)
	if !ok {
		client = transport.NewTaskClient(conn, handle.ID, res.Generation)
	}
	boot, err := client.GuestTaskBootID(ctx)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	var grant controlplane.SignedExecutionGrant
	if err = post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return substrate.ExecResult{}, err
	}
	check := func(g controlplane.SignedExecutionGrant) error {
		if err := controlplane.VerifyExecutionGrant(key, g, boot, time.Now()); err != nil {
			return err
		}
		digest, err := task.Digest()
		if err != nil {
			return err
		}
		x := g.Grant
		if x.AgentID != opts.ID || x.SessionID != opts.ExecutionSessionID || x.CommandID != cmd.ID || x.ActionID != cmd.ActionID || x.Attempt != uint64(cmd.Attempt) || x.Scope.TopologyUID != st.TopologyUID || x.Scope.ResourceUID != res.ResourceUID || x.Scope.Generation != res.Generation || x.Digest != digest || x.Operation != "guest_task" {
			return guesttask.ErrUnauthorized
		}
		return nil
	}
	if err = check(grant); err != nil {
		return substrate.ExecResult{}, err
	}
	req := substrate.ExecRequest{Program: task.Args[0], Args: task.Args[1:], Environment: task.Environment, WorkingDir: task.WorkingDir, Shell: substrate.ShellNone, OutputLimitBytes: task.OutputLimit}
	execution, err := client.Start(transport.WithTaskGrant(ctx, task.ID, grant), req)
	if err != nil {
		if execution.ID == "" {
			return substrate.ExecResult{}, err
		}
		if _, observeErr := client.ObserveExecution(ctx, execution); observeErr != nil {
			return substrate.ExecResult{}, fmt.Errorf("task start outcome unknown: %w", err)
		}
	}
	return awaitGrantedTask(ctx, opts, client, execution, endpoint, cmd.LeaseOwner, grant, check)
}

func awaitGrantedTask(ctx context.Context, opts Options, client durableGuestTaskConnection, handle substrate.ExecutionHandle, endpoint, leaseOwner string, grant controlplane.SignedExecutionGrant, check func(controlplane.SignedExecutionGrant) error) (substrate.ExecResult, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	budget, err := controlplane.ExecutionGrantBudget(grant, time.Now())
	if err != nil {
		return substrate.ExecResult{}, err
	}
	renewAt := time.Now().Add(budget / 3)
	cancelTask := func(cause error) (substrate.ExecResult, error) {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		termination, cancelErr := client.CancelExecution(cleanup, handle)
		result := substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: termination}
		if cancelErr != nil {
			return result, fmt.Errorf("task cancellation unconfirmed: %w", cause)
		}
		return result, cause
	}
	for {
		result, err := client.ObserveExecution(ctx, handle)
		if err != nil {
			return cancelTask(err)
		}
		if result.ExitCodeKnown {
			for _, stream := range []string{"stdout", "stderr"} {
				output, err := client.ReadExecutionOutput(ctx, handle, stream, 0, guestOutputLimit)
				if err != nil {
					return result, err
				}
				if stream == "stdout" {
					result.Stdout = string(output.Data)
				} else {
					result.Stderr = string(output.Data)
				}
			}
			return result, nil
		}
		if result.Status != substrate.ExecStatusRunning {
			return result, guesttask.ErrUnknown
		}
		if !time.Now().Before(renewAt) {
			var renewed controlplane.SignedExecutionGrant
			err = post(ctx, opts, endpoint+"/renew", map[string]any{"grant": grant, "lease_owner": leaseOwner}, &renewed)
			if err != nil {
				return cancelTask(err)
			}
			if err = check(renewed); err != nil {
				return cancelTask(err)
			}
			if renewed.Grant.Epoch != grant.Grant.Epoch {
				return cancelTask(guesttask.ErrConflict)
			}
			if err = client.RenewExecution(transport.WithTaskGrant(ctx, handle.ID, renewed), handle); err != nil {
				return cancelTask(err)
			}
			grant = renewed
			budget, err = controlplane.ExecutionGrantBudget(grant, time.Now())
			if err != nil {
				return cancelTask(err)
			}
			renewAt = time.Now().Add(budget / 3)
		}
		select {
		case <-ctx.Done():
			return cancelTask(ctx.Err())
		case <-ticker.C:
		}
	}
}
