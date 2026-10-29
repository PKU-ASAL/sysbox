package agentexec

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"os"
	"strings"
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

func executeGrantedGuestOperation(ctx context.Context, opts Options, cmd controlplane.AgentCommand) guestOperationCompletion {
	result, err := runGrantedGuestTask(ctx, opts, cmd)
	if err != nil {
		class := classifyGuestExecError(ctx, err)
		return guestOperationCompletion{Result: encodeGuestResult(result), ResultClass: class, Err: class + " guest operation"}
	}
	out := encodeGuestResult(result)
	out.ExitCode = result.ExitCode
	return guestOperationCompletion{Result: out, ResultClass: controlplane.GuestExecutionResultClassExit}
}

type grantedTask struct {
	client   *hostsupervisor.TaskClient
	handle   substrate.ExecutionHandle
	grant    controlplane.SignedExecutionGrant
	endpoint string
	check    func(controlplane.SignedExecutionGrant) error
}

func startGrantedTask(ctx context.Context, opts Options, cmd controlplane.AgentCommand) (*grantedTask, error) {
	if opts.HostSupervisorSocket == "" {
		return nil, errors.New("guest execution requires a separate host supervisor socket")
	}
	if opts.ExecutionSessionID == "" || opts.ExecutionGrantPublicKeyFile == "" {
		return nil, errors.New("guest execution requires an authenticated session and pinned grant issuer key")
	}
	key, err := os.ReadFile(opts.ExecutionGrantPublicKeyFile)
	if err != nil {
		return nil, err
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, controlplane.ErrInvalidExecutionGrant
	}
	var task guesttask.Request
	var topology, node string
	switch cmd.Type {
	case "guest_execution":
		task, err = guesttask.FromExecutionCommand(cmd)
		if cmd.Execution != nil {
			topology, node = cmd.Execution.Topology, cmd.Execution.Node
		}
	case "session_open":
		task, err = guesttask.FromConsoleCommand(cmd)
		if cmd.Session != nil {
			topology, node = cmd.Session.Topology, cmd.Session.Node
		}
	default:
		return nil, guesttask.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 || topology == "" || node == "" {
		return nil, guesttask.ErrConflict
	}
	client := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, hostsupervisor.TargetRef{Topology: topology, Node: node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}})
	boot, err := client.GuestTaskBootID(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := opts.APIURL + "/v1/agents/" + opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	var grant controlplane.SignedExecutionGrant
	if err = post(ctx, opts, endpoint, map[string]any{"session_id": opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return nil, err
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
		if x.AgentID != opts.ID || x.SessionID != opts.ExecutionSessionID || x.CommandID != cmd.ID || x.ActionID != cmd.ActionID || x.Attempt != uint64(cmd.Attempt) || x.Scope.TopologyUID != cmd.TopologyUID || x.Scope.ResourceUID != cmd.ResourceUID || x.Scope.Generation != cmd.Generation || x.Digest != digest || x.Operation != "guest_task" {
			return guesttask.ErrUnauthorized
		}
		return nil
	}
	if err = check(grant); err != nil {
		return nil, err
	}
	req := substrate.ExecRequest{Program: task.Args[0], Args: task.Args[1:], Environment: task.Environment, WorkingDir: task.WorkingDir, Shell: substrate.ShellNone, OutputLimitBytes: task.OutputLimit, Terminal: task.Terminal}
	execution, err := client.Start(transport.WithTaskGrant(ctx, task.ID, grant), req)
	if err != nil {
		if execution.ID == "" {
			return nil, err
		}
		if _, observeErr := client.ObserveExecution(ctx, execution); observeErr != nil {
			return nil, fmt.Errorf("task start outcome unknown: %w", err)
		}
	}
	return &grantedTask{client: client, handle: execution, endpoint: endpoint, grant: grant, check: check}, nil
}

func runGrantedGuestTask(ctx context.Context, opts Options, cmd controlplane.AgentCommand) (substrate.ExecResult, error) {
	task, err := startGrantedTask(ctx, opts, cmd)
	if err != nil {
		return substrate.ExecResult{}, err
	}
	return awaitGrantedTask(ctx, opts, task.client, task.handle, task.endpoint, cmd.LeaseOwner, task.grant, task.check)
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
				output, truncated, err := readGrantedTaskOutput(ctx, client, handle, stream)
				if stream == "stdout" {
					result.Stdout = output
					result.StdoutTruncated = result.StdoutTruncated || truncated
				} else {
					result.Stderr = output
					result.StderrTruncated = result.StderrTruncated || truncated
				}
				if err != nil {
					return result, err
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

// Each guest read is capped at 64 KiB. The collector retains at most the
// operation's output budget and probes one additional byte to distinguish an
// exact fit from truncation without copying excess output into API memory.
func readGrantedTaskOutput(ctx context.Context, client substrate.ConnectionExecutionOutput, handle substrate.ExecutionHandle, stream string) (string, bool, error) {
	var output strings.Builder
	var cursor int64
	for {
		if err := ctx.Err(); err != nil {
			return output.String(), false, err
		}
		remaining := guestOutputLimit - output.Len()
		limit := min(64<<10, remaining)
		if remaining == 0 {
			limit = 1
		}
		page, err := client.ReadExecutionOutput(ctx, handle, stream, cursor, limit)
		if err != nil {
			return output.String(), false, err
		}
		if len(page.Data) > limit || page.NextCursor != cursor+int64(len(page.Data)) {
			return output.String(), false, fmt.Errorf("invalid task output cursor or page size")
		}
		if remaining == 0 {
			return output.String(), len(page.Data) > 0, nil
		}
		output.Write(page.Data)
		cursor = page.NextCursor
		if len(page.Data) < limit {
			return output.String(), false, nil
		}
	}
}
