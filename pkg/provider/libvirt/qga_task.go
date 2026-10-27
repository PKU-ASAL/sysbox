package libvirt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// The immutable domain guard runs on every RPC. QGA starts only the installed
// helper; worker lifetime and cancellation belong to its durable Job Object.
func (c *qgaDomainConnection) taskCall(ctx context.Context, call guesttask.Call) (guesttask.Response, error) {
	var response guesttask.Response
	if err := c.validate(ctx); err != nil {
		return response, err
	}
	raw, err := json.Marshal(call)
	if err != nil {
		return response, err
	}
	if len(raw) > 32<<10 {
		return response, fmt.Errorf("Windows task call exceeds 32 KiB QGA envelope")
	}
	result, err := c.qga.Exec(ctx, substrate.ExecRequest{Program: `C:\ProgramData\Sysbox\guest-task\sysbox-windows-task.exe`, Args: []string{"--guest-task"}, Shell: substrate.ShellNone, Stdin: bytes.NewReader(raw), OutputLimitBytes: 2 << 20}, nil, nil)
	if err != nil {
		return response, err
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 || result.StdoutTruncated {
		return response, fmt.Errorf("Windows task helper unavailable or untrusted: %s", result.Stderr)
	}
	if err = json.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return response, err
	}
	if response.Version != guesttask.Version {
		return response, fmt.Errorf("Windows task helper version requires rebuild")
	}
	if response.Error != "" {
		return response, fmt.Errorf("Windows guest task: %s", response.Error)
	}
	return response, nil
}
func (c *qgaDomainConnection) tasks() *transport.TaskClient {
	return transport.NewTaskRPCClient(c.handle.ID, c.handle.Generation, c.taskCall)
}
func (c *qgaDomainConnection) Start(ctx context.Context, r substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	if r.CaptureViaFile {
		return substrate.ExecutionHandle{}, fmt.Errorf("durable Windows task output uses bounded protocol reads")
	}
	return c.tasks().Start(ctx, r)
}
func (c *qgaDomainConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return c.tasks().ObserveExecution(ctx, h)
}
func (c *qgaDomainConnection) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	return c.tasks().CancelExecution(ctx, h)
}
func (c *qgaDomainConnection) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	return c.tasks().ReadExecutionOutput(ctx, h, s, p, n)
}
func (c *qgaDomainConnection) ListExecutions(ctx context.Context) ([]substrate.ExecutionHandle, error) {
	return c.tasks().ListExecutions(ctx)
}
func (c *qgaDomainConnection) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	return c.tasks().RenewExecution(ctx, h)
}
func (c *qgaDomainConnection) GuestTaskBootID(ctx context.Context) (string, error) {
	return c.tasks().GuestTaskBootID(ctx)
}

var _ substrate.ConnectionStarter = (*qgaDomainConnection)(nil)
var _ substrate.ConnectionExecutionObserver = (*qgaDomainConnection)(nil)
var _ substrate.ConnectionExecutionCanceler = (*qgaDomainConnection)(nil)
