package guesttask

import (
	"context"
	"os"
	"path/filepath"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
)

type sshStopRecord struct {
	Version int                         `json:"version"`
	Grant   SignedGrant                 `json:"grant"`
	Request substrate.SSHAccessMutation `json:"request"`
	Stop    substrate.SSHAccessStop     `json:"stop"`
}

func (e *Engine) loadSSHStop(req substrate.SSHAccessMutation, g SignedGrant) (sshStopRecord, error) {
	var r sshStopRecord
	err := readJSON(filepath.Join(e.Root, ".ssh-stops", g.Grant.ActionID+".json"), &r)
	if err != nil {
		return r, err
	}
	digest, err := req.Digest()
	if err != nil || r.Version != 1 || !sameCancellationGrant(r.Grant.Grant, g.Grant) || r.Grant.BootID != g.BootID || r.Stop.Handle.TopologyUID != g.Grant.Scope.TopologyUID || r.Stop.Handle.ResourceUID != g.Grant.Scope.ResourceUID || r.Stop.Handle.Generation != g.Grant.Scope.Generation || r.Stop.Handle.ID != req.ID || r.Stop.Handle.Digest != digest || r.Request.ID != req.ID {
		return r, ErrConflict
	}
	rd, err := r.Request.Digest()
	if err != nil || rd != digest || r.Stop.Validate(g.Grant.Epoch, g.BootID, req) != nil {
		return r, ErrConflict
	}
	if controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, r.Grant.BootID, r.Grant.IssuedAt) != nil {
		return r, ErrUnauthorized
	}
	return r, nil
}

// StopSSHAccess waits for the exact helper mutation's scope/store locks. It
// persists a stop tombstone before reporting; absence of a committed record
// remains unknown. No new effect or revision is created by this operation.
func (e *Engine) StopSSHAccess(ctx context.Context, req substrate.SSHAccessMutation, g SignedGrant) (substrate.SSHAccessStop, error) {
	var zero substrate.SSHAccessStop
	digest, err := req.Digest()
	if err != nil {
		return zero, err
	}
	grant := g.Grant
	if grant.ActionID != req.ID || grant.Digest != digest || grant.Operation != sshAccessOperation(req.Delete) || grant.Scope.ResourceUID == "" || grant.Scope.Generation < 1 || controlplane.VerifyExecutionGrant(e.PublicKey, g, g.BootID, g.IssuedAt) != nil {
		return zero, ErrUnauthorized
	}
	// Exact durable stop replay need not acquire an obsolete scope epoch.
	if r, err := e.loadSSHStop(req, g); err == nil {
		return r.Stop, nil
	} else if !os.IsNotExist(err) {
		return zero, err
	}
	boot, err := BootID()
	if err != nil {
		return zero, err
	}
	if boot != g.BootID {
		return zero, ErrUnknown
	}
	scopePath, scope, release, err := e.lockScopeMode(ctx, grant, true)
	if err != nil {
		return zero, err
	}
	defer release()
	store := e.sshAccessStore()
	unlock, err := store.lock(ctx, "access")
	if err != nil {
		return zero, err
	}
	defer unlock()
	stops := Engine{Root: filepath.Join(e.Root, ".ssh-stops")}
	unlockStops, err := stops.lock(ctx, "stops")
	if err != nil {
		return zero, err
	}
	defer unlockStops()
	if r, err := e.loadSSHStop(req, g); err == nil {
		return r.Stop, nil
	} else if !os.IsNotExist(err) {
		return zero, err
	}
	stop := substrate.SSHAccessStop{Handle: substrate.SSHAccessHandle{ID: req.ID, Digest: digest, User: req.Config.User, AccessID: req.Config.AccessID, Revision: req.ExpectedRevision + 1, BootID: boot, TopologyUID: grant.Scope.TopologyUID, ResourceUID: grant.Scope.ResourceUID, Generation: grant.Scope.Generation, Epoch: grant.Epoch}, Quiescent: true, Outcome: "unknown"}
	parent, err := openInstallRoot(store.Root)
	if err != nil {
		return zero, err
	}
	defer parent.Close()
	record, readErr := e.readSSHAccess(parent, sshAccessName(req.Config.User, req.Config.AccessID))
	if readErr != nil && !os.IsNotExist(readErr) {
		return zero, readErr
	}
	if readErr == nil {
		if record.current().Grant.Grant.Scope != grant.Scope {
			return zero, ErrConflict
		}
		if old, ok := record.History[req.ID]; ok {
			if !sameCancellationGrant(old.Grant.Grant, grant) || old.Result.Handle != stop.Handle {
				return zero, ErrConflict
			}
			stop.Outcome = "committed"
			result := old.Result
			stop.Result = &result
		}
	}
	if err := stop.Validate(grant.Epoch, boot, req); err != nil {
		return zero, err
	}
	// Format 3 is rejected by older helpers under this same lock, including
	// old RPCs already dispatched before the new helper was installed.
	// Write this first: a crash is recoverable by the stop-only lock mode.
	scope.Version, scope.Epoch, scope.Session, scope.Stopped = 3, grant.Epoch, grant.SessionID, true
	if err := atomicJSON(scopePath, scope); err != nil {
		return zero, err
	}
	r := sshStopRecord{Version: 1, Grant: g, Request: req, Stop: stop}
	if err := atomicJSON(filepath.Join(stops.Root, req.ID+".json"), r); err != nil {
		return zero, err
	}
	return stop, nil
}
