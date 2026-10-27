package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"io"
)

func installGuestFile(ctx context.Context, conn taskCaller, identity executionIdentity, req substrate.FileInstallRequest, data io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	var handle substrate.FileInstallHandle
	var result substrate.FileInstallResult
	if err := req.Validate(); err != nil {
		return handle, result, err
	}
	auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization)
	if !ok || auth.ID != req.ID || identity.nodeID == "" || identity.generation < 1 || auth.Grant.Grant.Scope.Generation != identity.generation || auth.Grant.Grant.Operation != "guest_file_install" {
		return handle, result, guesttask.ErrUnauthorized
	}
	digest, err := req.Digest()
	if err != nil {
		return handle, result, err
	}
	if digest != auth.Grant.Grant.Digest || auth.Grant.BootID == "" || auth.Grant.Grant.Epoch == 0 {
		return handle, result, guesttask.ErrUnauthorized
	}
	if data == nil {
		return handle, result, fmt.Errorf("file installation data required")
	}
	raw, err := io.ReadAll(io.LimitReader(data, substrate.MaxFileInstallSize+1))
	if err != nil {
		return handle, result, err
	}
	if int64(len(raw)) != req.Size {
		return handle, result, fmt.Errorf("file installation size mismatch")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != req.SHA256 {
		return handle, result, fmt.Errorf("file installation checksum mismatch")
	}
	scope := auth.Grant.Grant.Scope
	handle = substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: auth.Grant.BootID, Epoch: auth.Grant.Grant.Epoch, NodeID: identity.nodeID, TopologyUID: scope.TopologyUID, ResourceUID: scope.ResourceUID, Generation: scope.Generation}
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "file-install", FileRequest: &req, Grant: &auth.Grant, FileData: raw})
	if err != nil {
		return handle, result, err
	}
	result, err = validateFileRecord(response, handle)
	return handle, result, err
}

func validateFileRecord(response guesttask.Response, handle substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	if response.FileRecord == nil {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	expected := handle
	expected.NodeID = ""
	if response.FileRecord.Handle != expected {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	if response.FileRecord.Request.ID == "" && response.FileRecord.Result.Outcome == substrate.FileInstallCancelled && response.FileRecord.Result.Quiescent {
		return response.FileRecord.Result, nil
	}
	digest, err := response.FileRecord.Request.Digest()
	if err != nil || digest != handle.Digest {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	result := response.FileRecord.Result
	if result.Outcome != substrate.FileInstallCommitted && result.Outcome != substrate.FileInstallCancelled && result.Outcome != substrate.FileInstallUnknown {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	if result.Outcome == substrate.FileInstallCommitted && (!result.Quiescent || result.SHA256 != response.FileRecord.Request.SHA256 || result.Size != response.FileRecord.Request.Size || result.Mode != response.FileRecord.Request.Mode) {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	return response.FileRecord.Result, nil
}

func fileRecord(ctx context.Context, conn taskCaller, identity executionIdentity, handle substrate.FileInstallHandle, operation string) (substrate.FileInstallResult, error) {
	if handle.NodeID != identity.nodeID || identity.nodeID == "" || handle.Generation != identity.generation || handle.Generation < 1 || handle.ID == "" || handle.Digest == "" || handle.BootID == "" || handle.Epoch == 0 || handle.TopologyUID == "" || handle.ResourceUID == "" {
		return substrate.FileInstallResult{}, guesttask.ErrConflict
	}
	guest := handle
	guest.NodeID = ""
	call := guesttask.Call{Version: guesttask.Version, Operation: operation, FileHandle: &guest}
	if operation == "file-cancel" || operation == "file-stop" {
		if auth, ok := ctx.Value(taskAuthorizationKey{}).(taskAuthorization); ok && auth.ID == handle.ID {
			call.Grant = &auth.Grant
		}
	}
	response, err := conn.taskCall(ctx, call)
	if err != nil {
		return substrate.FileInstallResult{}, err
	}
	return validateFileRecord(response, handle)
}

func fileBootID(ctx context.Context, conn taskCaller) (string, error) {
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "file-info"})
	if err != nil {
		return "", err
	}
	if response.BootID == "" {
		return "", guesttask.ErrUnknown
	}
	return response.BootID, nil
}
func (c *SSHConnection) GuestFileBootID(ctx context.Context) (string, error) {
	return fileBootID(ctx, c)
}
func (c *VsockConnection) GuestFileBootID(ctx context.Context) (string, error) {
	return fileBootID(ctx, c)
}
func (c *TaskClient) GuestFileBootID(ctx context.Context) (string, error) { return fileBootID(ctx, c) }
func (c *SSHConnection) InstallFile(ctx context.Context, r substrate.FileInstallRequest, data io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	return installGuestFile(ctx, c, c.executionIdentity, r, data)
}
func (c *VsockConnection) InstallFile(ctx context.Context, r substrate.FileInstallRequest, data io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	return installGuestFile(ctx, c, c.executionIdentity, r, data)
}
func (c *TaskClient) InstallFile(ctx context.Context, r substrate.FileInstallRequest, data io.Reader) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	return installGuestFile(ctx, c, c.identity, r, data)
}
func (c *SSHConnection) ObserveFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-observe")
}
func (c *VsockConnection) ObserveFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-observe")
}
func (c *TaskClient) ObserveFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.identity, h, "file-observe")
}
func (c *SSHConnection) CancelFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-cancel")
}
func (c *VsockConnection) CancelFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-cancel")
}
func (c *TaskClient) CancelFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.identity, h, "file-cancel")
}

func (c *SSHConnection) StopFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-stop")
}
func (c *VsockConnection) StopFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.executionIdentity, h, "file-stop")
}
func (c *TaskClient) StopFileInstall(ctx context.Context, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return fileRecord(ctx, c, c.identity, h, "file-stop")
}
