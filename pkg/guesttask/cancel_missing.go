package guesttask

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"os"
	"path/filepath"
)

func sameCancellationGrant(a, b controlplane.ExecutionGrant) bool {
	a.State = b.State
	a.StopReceipt = b.StopReceipt
	a.ExpiresAt = b.ExpiresAt
	return a == b
}

func (e *Engine) validateCancellationTombstone(r Record) error {
	proof := r.Cancellation
	if proof == nil || controlplane.VerifyExecutionCancellation(e.PublicKey, *proof) != nil {
		return ErrUnauthorized
	}
	g := proof.Grant
	if r.Grant.Grant != g || r.Grant.BootID != g.TargetBootID || len(r.Grant.Signature) != 0 || !r.Grant.IssuedAt.IsZero() || r.ID != g.ActionID || r.Digest != g.Digest || r.BootID != g.TargetBootID || !r.CancelRequested || !r.TerminationConfirmed || r.ExitCodeKnown || r.Status != "cancelled" || r.PID != 0 || r.StartTicks != 0 || r.SupervisorPID != 0 || r.SupervisorStartTicks != 0 || r.CgroupInode != 0 {
		return ErrConflict
	}
	return nil
}

func (e *Engine) cancelAuthorized(ctx context.Context, call Call) error {
	proof := call.Cancellation
	if proof == nil || controlplane.VerifyExecutionCancellation(e.PublicKey, *proof) != nil {
		return ErrUnauthorized
	}
	g := proof.Grant
	if call.ID != g.ActionID || call.Digest != g.Digest || call.BootID != g.TargetBootID || call.Epoch != g.Epoch || call.Scope == nil || *call.Scope != g.Scope {
		return ErrConflict
	}
	// Historical records do not acquire fresh scope authority. In particular,
	// retrying an old confirmed stop must not interfere with a successor epoch.
	unlock, err := e.lock(ctx, call.ID)
	if err != nil {
		return err
	}
	r, loadErr := e.load(call.ID)
	if loadErr == nil {
		defer unlock()
		if !sameCancellationGrant(r.Grant.Grant, g) || r.Digest != g.Digest || r.BootID != g.TargetBootID {
			return ErrConflict
		}
		return e.cancelLocked(r)
	}
	unlock()
	if !os.IsNotExist(loadErr) {
		return loadErr
	}
	boot, err := BootID()
	if err != nil {
		return err
	}
	if boot != g.TargetBootID {
		return ErrUnauthorized
	}
	path, scope, release, err := e.lockScope(ctx, g)
	if err != nil {
		return err
	}
	defer release()
	unlock, err = e.lock(ctx, call.ID)
	if err != nil {
		return err
	}
	defer unlock()
	r, loadErr = e.load(call.ID)
	if loadErr == nil {
		if !sameCancellationGrant(r.Grant.Grant, g) || r.Digest != g.Digest || r.BootID != g.TargetBootID {
			return ErrConflict
		}
		return e.cancelLocked(r)
	}
	if !os.IsNotExist(loadErr) {
		return loadErr
	}
	if err = e.verifyCgroupRoot(); err != nil {
		return err
	}
	// A cgroup without its record is an unknown outcome, not proof of absence.
	if _, err = os.Lstat(e.group(call.ID)); err == nil {
		return ErrUnknown
	} else if !os.IsNotExist(err) {
		return err
	}
	// ENOENT while following a dangling symlink is not an absent record.
	if info, err := os.Lstat(e.taskPath(call.ID)); err == nil && !info.IsDir() {
		return ErrUnknown
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(e.taskPath(call.ID), "record.json")); err == nil {
		return ErrUnknown
	} else if !os.IsNotExist(err) {
		return err
	}
	// The target directory may survive an interrupted initial record write.
	// Only this locked target with no record or cgroup is excluded from scanning.
	if err = e.scopeQuiescentExcept(g, call.ID); err != nil {
		return err
	}
	scope.Epoch, scope.Session = g.Epoch, g.SessionID
	if err = atomicJSON(path, scope); err != nil {
		return err
	}
	stamp, err := bootNanos()
	if err != nil {
		return err
	}
	r = Record{Version: RecordVersion, ID: call.ID, Digest: g.Digest, Grant: SignedGrant{Grant: g, BootID: boot}, BootID: boot, DeadlineBootNS: stamp, Status: "cancelled", CancelRequested: true, TerminationConfirmed: true, Cancellation: proof}
	return e.save(r)
}
