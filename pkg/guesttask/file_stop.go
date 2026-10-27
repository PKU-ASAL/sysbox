package guesttask

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

type fileStopRecord struct {
	Version int                         `json:"version"`
	Grant   SignedGrant                 `json:"grant"`
	Handle  substrate.FileInstallHandle `json:"handle"`
	Result  substrate.FileInstallResult `json:"result"`
}

func (e *Engine) loadFileStop(h substrate.FileInstallHandle) (fileStopRecord, error) {
	var r fileStopRecord
	err := readJSON(filepath.Join(e.Root, ".file-stops", h.ID+".json"), &r)
	if err != nil {
		return r, err
	}
	g := r.Grant.Grant
	if r.Version != 1 || r.Handle != h || r.Grant.BootID != h.BootID || g.ActionID != h.ID || g.Digest != h.Digest || g.Operation != "guest_file_install" || g.Epoch != h.Epoch || g.Scope.TopologyUID != h.TopologyUID || g.Scope.ResourceUID != h.ResourceUID || g.Scope.Generation != h.Generation || !r.Result.Quiescent || r.Result.Outcome != substrate.FileInstallUnknown || r.Result.StoppedEpoch != h.Epoch || controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, h.BootID, r.Grant.IssuedAt) != nil {
		return r, ErrConflict
	}
	boot, err := BootID()
	if err != nil {
		return r, err
	}
	if boot != h.BootID {
		return r, ErrUnknown
	}
	original, err := e.loadInstall(h.ID)
	if err != nil {
		return r, err
	}
	if original.Handle != h || !sameCancellationGrant(original.Grant.Grant, g) || r.Result.SHA256 != original.Request.SHA256 || r.Result.Size != original.Request.Size || r.Result.Mode != original.Request.Mode {
		return r, ErrConflict
	}
	return r, nil
}

// StopFileInstall fences future admission and waits for the exact writer lock.
// It never infers the installation result, changes the target, or cleans staging.
// The same boot and a kernel-local filesystem are required; reboot/NFS/FUSE
// cannot inherit this cessation proof.
func (e *Engine) StopFileInstall(ctx context.Context, h substrate.FileInstallHandle, g SignedGrant) (FileInstallRecord, error) {
	var zero FileInstallRecord
	grant := g.Grant
	if h.NodeID != "" || h.ID == "" || h.Epoch == 0 || g.BootID != h.BootID || grant.ActionID != h.ID || grant.Digest != h.Digest || grant.Epoch != h.Epoch || grant.Operation != "guest_file_install" || grant.Scope.TopologyUID != h.TopologyUID || grant.Scope.ResourceUID != h.ResourceUID || grant.Scope.Generation != h.Generation || controlplane.VerifyExecutionGrant(e.PublicKey, g, h.BootID, g.IssuedAt) != nil {
		return zero, ErrUnauthorized
	}
	if stopped, err := e.loadFileStop(h); err == nil {
		original, err := e.loadInstall(h.ID)
		if err != nil || original.Handle != h || !sameCancellationGrant(stopped.Grant.Grant, grant) {
			return zero, ErrConflict
		}
		original.Result = stopped.Result
		return original, nil
	} else if !os.IsNotExist(err) {
		return zero, err
	}
	boot, err := BootID()
	if err != nil {
		return zero, err
	}
	if boot != h.BootID {
		return zero, ErrUnknown
	}
	original, err := e.loadInstall(h.ID)
	if err != nil {
		return zero, err
	}
	if original.Handle != h || !sameCancellationGrant(original.Grant.Grant, grant) {
		return zero, ErrConflict
	}
	if err = e.requestInstallCancellation(ctx, h); err != nil {
		return zero, err
	}
	path, scope, release, err := e.lockScopeMode(ctx, grant, true)
	if err != nil {
		return zero, err
	}
	defer release()
	store := e.installStore()
	unlock, err := store.lock(ctx, h.ID)
	if err != nil {
		return zero, err
	}
	defer unlock()
	original, err = e.loadInstall(h.ID)
	if err != nil {
		return zero, err
	}
	if original.Handle != h || !sameCancellationGrant(original.Grant.Grant, grant) {
		return zero, ErrConflict
	}
	if original.Result.Quiescent {
		return original, nil
	}
	parent, err := e.installParent(original.Request.Path)
	if err != nil {
		return zero, err
	}
	defer parent.Close()
	identity, err := installFDIdentity(parent)
	if err != nil || identity != original.Parent {
		return zero, ErrConflict
	}
	if err = requireLocalFileStopFS(parent); err != nil {
		return zero, err
	}
	control, err := os.Open(e.Root)
	if err != nil {
		return zero, err
	}
	defer control.Close()
	if err = requireLocalFileStopFS(control); err != nil {
		return zero, err
	}
	result := installResult(original, substrate.FileInstallUnknown, true)
	result.StoppedEpoch = h.Epoch
	scope.Version, scope.Epoch, scope.Session, scope.Stopped = 3, grant.Epoch, grant.SessionID, true
	if err = atomicJSON(path, scope); err != nil {
		return zero, err
	}
	if err = atomicJSON(filepath.Join(e.Root, ".file-stops", h.ID+".json"), fileStopRecord{Version: 1, Grant: g, Handle: h, Result: result}); err != nil {
		return zero, err
	}
	original.Result = result
	return original, nil
}

// Both the target and lock/journal directory must use kernel-local storage.
func requireLocalFileStopFS(f *os.File) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &fs); err != nil {
		return err
	}
	if fs.Type != unix.TMPFS_MAGIC && fs.Type != unix.EXT4_SUPER_MAGIC {
		return ErrUnknown
	}
	return nil
}
