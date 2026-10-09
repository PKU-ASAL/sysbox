package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

func inspectInstalledFile(ctx context.Context, conn taskCaller, identity executionIdentity, handle substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	unknown := substrate.InstalledFileInspection{Status: "unknown"}
	if handle.NodeID != identity.nodeID || identity.nodeID == "" || handle.Generation != identity.generation || handle.Generation < 1 || handle.ID == "" || handle.Digest == "" || handle.BootID == "" || handle.Epoch == 0 || handle.TopologyUID == "" || handle.ResourceUID == "" {
		return unknown, guesttask.ErrConflict
	}
	guest := handle
	guest.NodeID = ""
	response, err := conn.taskCall(ctx, guesttask.Call{Version: guesttask.Version, Operation: "file-inspect", FileHandle: &guest})
	if err != nil {
		return unknown, err
	}
	if response.FileInspection == nil || response.FileHandle == nil || *response.FileHandle != guest {
		return unknown, guesttask.ErrConflict
	}
	result := *response.FileInspection
	if result.Status != "present" && result.Status != "drifted" && result.Status != "absent" {
		return unknown, guesttask.ErrUnknown
	}
	return result, nil
}

func (c *SSHConnection) InspectInstalledFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	return inspectInstalledFile(ctx, c, c.executionIdentity, h)
}
func (c *VsockConnection) InspectInstalledFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	return inspectInstalledFile(ctx, c, c.executionIdentity, h)
}
func (c *TaskClient) InspectInstalledFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	return inspectInstalledFile(ctx, c, c.identity, h)
}
