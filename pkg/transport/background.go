package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/vsockrpc"
	"io"
)

type executionIdentity struct {
	nodeID     string
	generation int64
}

func (i *executionIdentity) BindExecutionIdentity(nodeID string, generation int64) {
	i.nodeID, i.generation = nodeID, generation
}

type taskAuthorization struct {
	ID    string
	Grant controlplane.SignedExecutionGrant
}
type taskAuthorizationKey struct{}

// WithTaskGrant supplies control-plane authority; it never signs or renews it.
func WithTaskGrant(ctx context.Context, id string, grant controlplane.SignedExecutionGrant) context.Context {
	return context.WithValue(ctx, taskAuthorizationKey{}, taskAuthorization{ID: id, Grant: grant})
}

// BuildTaskRequest is shared by signing callers and transports. Request ID is
// assigned durably before transmission; retries must reuse ID and bytes.
func BuildTaskRequest(ctx context.Context, id string, req substrate.ExecRequest) (guesttask.Request, error) {
	args, err := CommandArgv(req)
	if err != nil {
		return guesttask.Request{}, err
	}
	input, err := readVsockStdin(ctx, req.Stdin)
	if err != nil {
		return guesttask.Request{}, err
	}
	limit := req.OutputLimitBytes
	if limit == 0 {
		limit = substrate.DefaultExecOutputLimit
	}
	task := guesttask.Request{UserID: 65534, ID: id, Args: args, Environment: req.Environment, WorkingDir: req.WorkingDir, Input: input, OutputLimit: limit}
	return task, task.Validate()
}

type taskCaller interface {
	taskCall(context.Context, guesttask.Call) (guesttask.Response, error)
}

func taskBootID(ctx context.Context, conn taskCaller) (string, error) {
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "info"})
	if err != nil {
		return "", err
	}
	if response.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return response.BootID, nil
}
func (c *SSHConnection) GuestTaskBootID(ctx context.Context) (string, error) {
	return taskBootID(ctx, c)
}
func (c *VsockConnection) GuestTaskBootID(ctx context.Context) (string, error) {
	return taskBootID(ctx, c)
}
func renewTask(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle) error {
	if _, err := taskRecord(ctx, conn, identity, h, "observe"); err != nil {
		return err
	}
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	if !ok || auth.ID != h.ID {
		return guesttask.ErrUnauthorized
	}
	_, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "renew", ID: h.ID, Digest: h.Digest, Grant: &auth.Grant})
	return err
}
func (c *SSHConnection) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	return renewTask(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	return renewTask(ctx, c, c.executionIdentity, h)
}

func startTask(ctx context.Context, conn taskCaller, identity executionIdentity, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	if !ok || identity.nodeID == "" || identity.generation < 1 || identity.generation != auth.Grant.Grant.Scope.Generation {
		return substrate.ExecutionHandle{}, fmt.Errorf("persistent guest task requires bound node and signed execution grant")
	}
	task, err := BuildTaskRequest(ctx, auth.ID, req)
	if err != nil {
		return substrate.ExecutionHandle{}, err
	}
	task.Grant = auth.Grant
	digest, err := task.Digest()
	if err != nil || digest != auth.Grant.Grant.Digest {
		return substrate.ExecutionHandle{}, guesttask.ErrUnauthorized
	}
	handle := substrate.ExecutionHandle{ID: task.ID, Digest: digest, NodeID: identity.nodeID, Generation: identity.generation, BootID: auth.Grant.BootID, Epoch: auth.Grant.Grant.Epoch, TopologyUID: auth.Grant.Grant.Scope.TopologyUID, ResourceUID: auth.Grant.Grant.Scope.ResourceUID}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "start", Request: &task})
	if err != nil {
		return handle, err
	}
	if response.Record == nil || response.Record.ID != handle.ID || response.Record.Digest != digest {
		return handle, guesttask.ErrConflict
	}
	handle.PID = response.Record.PID
	return handle, nil
}
func taskRecord(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle, op string) (*guesttask.Record, error) {
	if h.ID == "" || h.Digest == "" || h.NodeID != identity.nodeID || h.Generation != identity.generation || h.BootID == "" || h.Epoch == 0 {
		return nil, guesttask.ErrConflict
	}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: op, ID: h.ID, Digest: h.Digest, BootID: h.BootID, Epoch: h.Epoch, Scope: &controlplane.ExecutionScope{TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: h.Generation}})
	if err != nil {
		return nil, err
	}
	r := response.Record
	if r == nil || r.ID != h.ID || r.Digest != h.Digest || r.BootID != h.BootID || r.Grant.Grant.Scope.Generation != h.Generation || r.Grant.Grant.Epoch != h.Epoch {
		return nil, guesttask.ErrConflict
	}
	return r, nil
}
func observeTask(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	r, err := taskRecord(ctx, conn, identity, h, "observe")
	if err != nil {
		return substrate.ExecResult{}, err
	}
	result := substrate.ExecResult{ExitCode: r.ExitCode, ExitCodeKnown: r.ExitCodeKnown, Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationUnknown, StdoutTruncated: r.StdoutTruncated, StderrTruncated: r.StderrTruncated}
	if r.TerminationConfirmed {
		result.RemoteTermination = substrate.RemoteTerminationConfirmed
	}
	if r.ExitCodeKnown {
		result.Status = substrate.ExecStatusExited
	} else if r.Status == "running" {
		result.Status = substrate.ExecStatusRunning
	}
	return result, nil
}
func cancelTask(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	r, err := taskRecord(ctx, conn, identity, h, "cancel")
	if err != nil {
		return substrate.RemoteTerminationUnknown, err
	}
	if r.TerminationConfirmed {
		return substrate.RemoteTerminationConfirmed, nil
	}
	return substrate.RemoteTerminationUnknown, nil
}
func outputTask(ctx context.Context, conn taskCaller, identity executionIdentity, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	if _, err := taskRecord(ctx, conn, identity, h, "observe"); err != nil {
		return substrate.ExecutionOutput{}, err
	}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "output", ID: h.ID, Digest: h.Digest, Stream: s, Cursor: p, Limit: n})
	return substrate.ExecutionOutput{Data: response.Output, NextCursor: response.NextCursor}, err
}
func listTasks(ctx context.Context, conn taskCaller, identity executionIdentity) ([]substrate.ExecutionHandle, error) {
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "list"})
	if err != nil {
		return nil, err
	}
	var out []substrate.ExecutionHandle
	for _, r := range response.Records {
		if r.Grant.Grant.Scope.Generation == identity.generation {
			out = append(out, substrate.ExecutionHandle{ID: r.ID, Digest: r.Digest, NodeID: identity.nodeID, Generation: identity.generation, PID: r.PID, BootID: r.BootID, Epoch: r.Grant.Grant.Epoch, TopologyUID: r.Grant.Grant.Scope.TopologyUID, ResourceUID: r.Grant.Grant.Scope.ResourceUID})
		}
	}
	return out, nil
}
func (c *SSHConnection) taskCall(ctx context.Context, call guesttask.Call) (guesttask.Response, error) {
	return helperTaskCall(ctx, c, call)
}

func helperTaskCall(ctx context.Context, conn substrate.Connection, call guesttask.Call) (guesttask.Response, error) {
	raw, err := json.Marshal(call)
	if err != nil {
		return guesttask.Response{}, err
	}
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "/usr/local/bin/sysbox-guest-task", Args: []string{"--guest-task"}, Shell: substrate.ShellNone, Stdin: bytes.NewReader(raw), OutputLimitBytes: 2 * guesttask.MaxOutput}, nil, nil)
	if err != nil {
		return guesttask.Response{}, err
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 || result.StdoutTruncated {
		return guesttask.Response{}, fmt.Errorf("install current sysbox-guest-task helper and grant verification key before executing tasks")
	}
	return decodeTaskResponse([]byte(result.Stdout))
}

// TaskClient uses a provider's guarded foreground connection to reach the
// exact same installed helper as SSH. It does not detach a daemon exec handle.
type TaskClient struct {
	conn     substrate.Connection
	identity executionIdentity
}

func NewTaskClient(conn substrate.Connection, nodeID string, generation int64) *TaskClient {
	return &TaskClient{conn: conn, identity: executionIdentity{nodeID: nodeID, generation: generation}}
}
func (c *TaskClient) taskCall(ctx context.Context, call guesttask.Call) (guesttask.Response, error) {
	return helperTaskCall(ctx, c.conn, call)
}
func (c *TaskClient) Start(ctx context.Context, req substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	return startTask(ctx, c, c.identity, req)
}
func (c *TaskClient) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return observeTask(ctx, c, c.identity, h)
}
func (c *TaskClient) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	return cancelTask(ctx, c, c.identity, h)
}
func (c *TaskClient) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	return outputTask(ctx, c, c.identity, h, s, p, n)
}
func (c *TaskClient) ListExecutions(ctx context.Context) ([]substrate.ExecutionHandle, error) {
	return listTasks(ctx, c, c.identity)
}
func (c *TaskClient) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	return renewTask(ctx, c, c.identity, h)
}
func (c *TaskClient) GuestTaskBootID(ctx context.Context) (string, error) { return taskBootID(ctx, c) }
func (c *VsockConnection) taskCall(ctx context.Context, call guesttask.Call) (guesttask.Response, error) {
	raw, err := json.Marshal(call)
	if err != nil {
		return guesttask.Response{}, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return guesttask.Response{}, err
	}
	defer conn.Close()
	defer abortConnOnContext(ctx, conn)()
	if err = json.NewEncoder(conn).Encode(vsockrpc.Request{Op: vsockrpc.OpTask, Size: int64(len(raw))}); err != nil {
		return guesttask.Response{}, err
	}
	if _, err = io.Copy(conn, bytes.NewReader(raw)); err != nil {
		return guesttask.Response{}, err
	}
	var frame vsockrpc.Frame
	if err = json.NewDecoder(io.LimitReader(conn, 2*guesttask.MaxOutput)).Decode(&frame); err != nil {
		return guesttask.Response{}, err
	}
	if !frame.Done || frame.Error != "" {
		return guesttask.Response{}, fmt.Errorf("guest task protocol failed: %s", frame.Error)
	}
	return decodeTaskResponse(frame.Task)
}
func decodeTaskResponse(raw []byte) (guesttask.Response, error) {
	var response guesttask.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, err
	}
	if response.Version != guesttask.Version {
		return response, fmt.Errorf("guest task helper version requires rebuild")
	}
	if response.Error != "" {
		return response, fmt.Errorf("guest task: %s", response.Error)
	}
	return response, nil
}
func (c *SSHConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return observeTask(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	return observeTask(ctx, c, c.executionIdentity, h)
}
func (c *SSHConnection) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	return cancelTask(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	return cancelTask(ctx, c, c.executionIdentity, h)
}
func (c *SSHConnection) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	return outputTask(ctx, c, c.executionIdentity, h, s, p, n)
}
func (c *VsockConnection) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, s string, p int64, n int) (substrate.ExecutionOutput, error) {
	return outputTask(ctx, c, c.executionIdentity, h, s, p, n)
}
func (c *SSHConnection) ListExecutions(ctx context.Context) ([]substrate.ExecutionHandle, error) {
	return listTasks(ctx, c, c.executionIdentity)
}
func (c *VsockConnection) ListExecutions(ctx context.Context) ([]substrate.ExecutionHandle, error) {
	return listTasks(ctx, c, c.executionIdentity)
}
