package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"io"
	"net"
)

type TaskClient struct {
	socket string
	target TargetRef
}

func NewTaskClient(socket string, target TargetRef) *TaskClient {
	return &TaskClient{socket: socket, target: target}
}
func (c *TaskClient) call(ctx context.Context, request Request) (Response, error) {
	if c.socket == "" {
		return Response{}, fmt.Errorf("host supervisor socket required")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	request.Version = 1
	request.Target = c.target
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Response{}, err
	}
	var result Response
	if err := json.NewDecoder(io.LimitReader(conn, 16<<20)).Decode(&result); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	if result.Version != 1 {
		return result, fmt.Errorf("host supervisor protocol mismatch")
	}
	if result.Error != "" {
		return result, fmt.Errorf("host supervisor: %s", result.Error)
	}
	return result, nil
}
func (c *TaskClient) GuestTaskBootID(ctx context.Context) (string, error) {
	out, err := c.call(ctx, Request{Operation: "info"})
	if err != nil {
		return "", err
	}
	if out.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return out.BootID, nil
}
func (c *TaskClient) Start(ctx context.Context, request substrate.ExecRequest) (substrate.ExecutionHandle, error) {
	id, grant, ok := transport.TaskGrantFromContext(ctx)
	if !ok {
		return substrate.ExecutionHandle{}, guesttask.ErrUnauthorized
	}
	task, err := transport.BuildTaskRequest(ctx, id, request)
	if err != nil {
		return substrate.ExecutionHandle{}, err
	}
	task.Grant = grant
	// Return a complete identity even when the reply is lost, so the caller
	// can observe the write-ahead record instead of inventing another task ID.
	digest, err := task.Digest()
	if err != nil {
		return substrate.ExecutionHandle{}, err
	}
	handle := substrate.ExecutionHandle{ID: id, Digest: digest, NodeID: c.target.Node, Generation: c.target.Scope.Generation, TopologyUID: c.target.Scope.TopologyUID, ResourceUID: c.target.Scope.ResourceUID, BootID: grant.BootID, Epoch: grant.Grant.Epoch}
	out, err := c.call(ctx, Request{Operation: "start", Task: &task})
	if err != nil {
		return handle, err
	}
	return out.Handle, nil
}
func (c *TaskClient) ObserveExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.ExecResult, error) {
	out, err := c.call(ctx, Request{Operation: "observe", Handle: h})
	return out.Result, err
}
func (c *TaskClient) CancelExecution(ctx context.Context, h substrate.ExecutionHandle) (substrate.RemoteTermination, error) {
	out, err := c.call(ctx, Request{Operation: "cancel", Handle: h})
	return out.Result.RemoteTermination, err
}
func (c *TaskClient) ReadExecutionOutput(ctx context.Context, h substrate.ExecutionHandle, stream string, cursor int64, limit int) (substrate.ExecutionOutput, error) {
	out, err := c.call(ctx, Request{Operation: "output", Handle: h, Stream: stream, Cursor: cursor, Limit: limit})
	return out.Output, err
}
func (c *TaskClient) RenewExecution(ctx context.Context, h substrate.ExecutionHandle) error {
	id, grant, ok := transport.TaskGrantFromContext(ctx)
	if !ok || id != h.ID {
		return guesttask.ErrUnauthorized
	}
	_, err := c.call(ctx, Request{Operation: "renew", Handle: h, Grant: &grant})
	return err
}

func (c *TaskClient) HostBootID(ctx context.Context) (string, error) {
	out, err := c.call(ctx, Request{Operation: "host-info"})
	if err != nil {
		return "", err
	}
	if out.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return out.BootID, nil
}

func (c *TaskClient) RunHostBootID(ctx context.Context) (string, error) {
	out, err := c.call(ctx, Request{Operation: "run-info"})
	if err != nil {
		return "", err
	}
	if out.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return out.BootID, nil
}

func (c *TaskClient) AdoptionHostBootID(ctx context.Context) (string, error) {
	r, err := c.call(ctx, Request{Operation: "adoption-info"})
	if err != nil {
		return "", err
	}
	if r.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return r.BootID, nil
}
func (c *TaskClient) ExecutePower(ctx context.Context, req controlplane.NodePowerRequest, g controlplane.SignedExecutionGrant) error {
	_, err := c.call(ctx, Request{Operation: "power-apply", Power: &req, Grant: &g})
	return err
}
func (c *TaskClient) ObservePower(ctx context.Context, g controlplane.SignedExecutionGrant) error {
	_, err := c.call(ctx, Request{Operation: "power-observe", Grant: &g})
	return err
}

func (c *TaskClient) GuestFileBootID(ctx context.Context) (string, error) {
	r, err := c.call(ctx, Request{Operation: "file-info"})
	if err != nil {
		return "", err
	}
	if r.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return r.BootID, nil
}
func (c *TaskClient) InstallGuestFile(ctx context.Context, req substrate.FileInstallRequest, g controlplane.SignedExecutionGrant) (substrate.FileInstallResult, error) {
	r, err := c.call(ctx, Request{Operation: "file-install", FileRequest: &req, Grant: &g})
	return r.FileResult, err
}
func (c *TaskClient) ObserveGuestFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	r, err := c.call(ctx, Request{Operation: "file-observe", FileHandle: h})
	return r.FileResult, err
}
func (c *TaskClient) CancelGuestFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	r, err := c.call(ctx, Request{Operation: "file-cancel", FileHandle: h})
	return r.FileResult, err
}

func (c *TaskClient) StartRun(ctx context.Context, req RunRequest, grant controlplane.SignedExecutionGrant) (RunObservation, error) {
	r, err := c.call(ctx, Request{Operation: "run-start", RunRequest: &req, Grant: &grant})
	return runResponse(r, err)
}
func (c *TaskClient) ObserveRun(ctx context.Context, req RunRequest, grant controlplane.SignedExecutionGrant) (RunObservation, error) {
	r, err := c.call(ctx, Request{Operation: "run-observe", RunRequest: &req, Grant: &grant})
	return runResponse(r, err)
}
func runResponse(r Response, err error) (RunObservation, error) {
	if err != nil {
		return RunObservation{}, err
	}
	if r.RunObservation == nil {
		return RunObservation{}, fmt.Errorf("missing host Run observation")
	}
	switch r.RunObservation.Status {
	case "running", "completed", "uncertain":
	default:
		return RunObservation{}, fmt.Errorf("invalid host Run observation")
	}
	return *r.RunObservation, nil
}
func (c *TaskClient) RenewRun(ctx context.Context, req RunRequest, grant controlplane.SignedExecutionGrant) error {
	_, err := c.call(ctx, Request{Operation: "run-renew", RunRequest: &req, Grant: &grant})
	return err
}
func (c *TaskClient) CancelRun(ctx context.Context, req RunRequest, grant controlplane.SignedExecutionGrant) error {
	_, err := c.call(ctx, Request{Operation: "run-cancel", RunRequest: &req, Grant: &grant})
	return err
}

func (c *TaskClient) DeleteGC(ctx context.Context, candidate GCCandidate, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "gc-delete", GCCandidate: &candidate, Grant: &grant})
	return r.GCResult, err
}

func (c *TaskClient) ObserveGC(ctx context.Context, candidate GCCandidate, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "gc-observe", GCCandidate: &candidate, Grant: &grant})
	return r.GCResult, err
}

func (c *TaskClient) MutateGuestSSHAccess(ctx context.Context, req substrate.SSHAccessMutation, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "ssh-guest-mutate", SSHMutation: &req, Grant: &grant})
	return r.SSHEvidence, err
}
func (c *TaskClient) ObserveGuestSSHAccess(ctx context.Context, req substrate.SSHAccessMutation, grant controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "ssh-guest-observe", SSHMutation: &req, Grant: &grant})
	return r.SSHEvidence, err
}
func (c *TaskClient) ApplyAdoption(ctx context.Context, req controlplane.AdoptionRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "adoption-apply", AdoptionRequest: &req, Grant: &g})
	return r.AdoptionEvidence, err
}
func (c *TaskClient) ObserveAdoption(ctx context.Context, req controlplane.AdoptionRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "adoption-observe", AdoptionRequest: &req, Grant: &g})
	return r.AdoptionEvidence, err
}

func (c *TaskClient) GCHostBootID(ctx context.Context) (string, error) {
	r, err := c.call(ctx, Request{Operation: "gc-info"})
	return r.BootID, err
}

func (c *TaskClient) InspectAdoption(ctx context.Context, req controlplane.AdoptionInspectionRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	r, err := c.call(ctx, Request{Operation: "adoption-inspect", AdoptionInspection: &req, Grant: &g})
	return r.AdoptionEvidence, err
}
