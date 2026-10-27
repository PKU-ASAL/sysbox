package hostsupervisor

import (
	"context"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Inspect resolves current canonical resource identity before reading the
// target of a committed installation. Unlike Observe it never reports or alters
// a historical receipt, and it cannot turn an uncertain install into a commit.
func (d *FileDispatcher) Inspect(ctx context.Context, ref TargetRef, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	unknown := substrate.InstalledFileInspection{Status: "unknown"}
	prior, err := d.loadFile(h)
	if err != nil {
		return unknown, err
	}
	if prior.Ref != ref || !prior.Dispatched || prior.Result.Outcome != substrate.FileInstallCommitted || !prior.Result.Quiescent || prior.Receipt == nil {
		return unknown, guesttask.ErrConflict
	}
	target, err := d.target(ctx, ref, prior.Grant.Grant)
	if err != nil {
		return unknown, err
	}
	if target.NodeID != prior.ProviderNodeID {
		return unknown, guesttask.ErrConflict
	}
	inspector, ok := target.Connection.(substrate.ConnectionInstalledFileInspector)
	if !ok {
		return unknown, substrate.ErrNotSupported
	}
	providerHandle := h
	providerHandle.NodeID = target.NodeID
	result, err := inspector.InspectInstalledFile(ctx, providerHandle)
	if err != nil {
		return unknown, err
	}
	if result.Status != "present" && result.Status != "drifted" && result.Status != "absent" {
		return unknown, guesttask.ErrUnknown
	}
	if result.Status == "present" && (result.SHA256 != prior.Request.SHA256 || result.Size != prior.Request.Size || result.Mode != prior.Request.Mode) {
		return unknown, guesttask.ErrConflict
	}
	// A slow guest read must not be returned as current for a replaced node.
	current, err := d.target(ctx, ref, prior.Grant.Grant)
	if err != nil {
		return unknown, err
	}
	if current.NodeID != prior.ProviderNodeID {
		return unknown, guesttask.ErrConflict
	}
	return result, nil
}

func (c *TaskClient) InspectInstalledFile(ctx context.Context, h substrate.FileInstallHandle) (substrate.InstalledFileInspection, error) {
	unknown := substrate.InstalledFileInspection{Status: "unknown"}
	response, err := c.call(ctx, Request{Operation: "file-inspect", FileHandle: h})
	if err != nil {
		return unknown, err
	}
	if response.FileInspection == nil || response.InspectedFile == nil || *response.InspectedFile != h {
		return unknown, guesttask.ErrConflict
	}
	return *response.FileInspection, nil
}
