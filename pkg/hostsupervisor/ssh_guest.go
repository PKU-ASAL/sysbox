package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

type sshGuestRecord struct {
	RunID          string                                   `json:"run_id,omitempty"`
	Version        int                                      `json:"version"`
	Ref            TargetRef                                `json:"ref"`
	Request        substrate.SSHAccessMutation              `json:"request"`
	Handle         substrate.SSHAccessHandle                `json:"handle"`
	ProviderNodeID string                                   `json:"provider_node_id"`
	Grant          controlplane.SignedExecutionGrant        `json:"grant"`
	Result         substrate.SSHAccessResult                `json:"result"`
	Receipt        *controlplane.SignedExecutionStopReceipt `json:"receipt,omitempty"`
	Reported       bool                                     `json:"reported"`
}

// 64 keys of up to 16 KiB can expand sixfold when JSON escapes comments.
// Reserve room for identity metadata and the signed completion receipt too.
const maxSSHGuestRecordBytes = 8 << 20

// SSHGuestDispatcher uses a private journal root. Mutation and recovery of an
// action hold the same process-independent lock until evidence is persisted.
type SSHGuestDispatcher struct {
	*Dispatcher
	Report        func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
	ReportDerived func(context.Context, substrate.SSHAccessMutation, controlplane.SignedExecutionStopReceipt) error
}

func sshExecutionHandle(h substrate.SSHAccessHandle) substrate.ExecutionHandle {
	return substrate.ExecutionHandle{ID: h.ID, Digest: h.Digest, NodeID: h.NodeID, BootID: h.BootID, TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: h.Generation, Epoch: h.Epoch}
}
func sshGuestPath(root string, h substrate.SSHAccessHandle) string {
	return filepath.Join(root, recordKey(sshExecutionHandle(h))+".json")
}
func (d *SSHGuestDispatcher) configured() error {
	if d == nil || d.Dispatcher == nil || (d.Report == nil && d.ReportDerived == nil) || len(d.IssuerKey) != ed25519.PublicKeySize || len(d.HostKey) != ed25519.PrivateKeySize {
		return effectgate.ErrFenced
	}
	return nil
}
func sshResultHandle(ref TargetRef, req substrate.SSHAccessMutation, g controlplane.SignedExecutionGrant) (substrate.SSHAccessHandle, error) {
	digest, err := req.Digest()
	if err != nil {
		return substrate.SSHAccessHandle{}, err
	}
	op := "guest_ssh_access_apply"
	if req.Delete {
		op = "guest_ssh_access_delete"
	}
	if req.ExpectedRevision == math.MaxUint64 || g.Grant.Operation != op || g.Grant.Digest != digest || g.Grant.ActionID != req.ID || g.Grant.Scope != ref.Scope || ref.Topology == "" || ref.Node == "" || g.BootID == "" || g.Grant.TargetBootID != g.BootID {
		return substrate.SSHAccessHandle{}, effectgate.ErrFenced
	}
	return substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: ref.Node, User: req.Config.User, AccessID: req.Config.AccessID, Revision: req.ExpectedRevision + 1, BootID: g.BootID, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: g.Grant.Epoch}, nil
}
func (d *SSHGuestDispatcher) saveSSH(r sshGuestRecord) error {
	return saveRecord(d.Root, sshGuestPath(d.Root, r.Handle), r)
}
func (d *SSHGuestDispatcher) loadSSH(path string) (sshGuestRecord, error) {
	var r sshGuestRecord
	if err := ValidateOwnedPath(path, true); err != nil {
		return r, err
	}
	file, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSSHGuestRecordBytes+1))
	if err != nil {
		return r, err
	}
	if len(raw) > maxSSHGuestRecordBytes {
		return r, guesttask.ErrConflict
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	h, err := sshResultHandle(r.Ref, r.Request, r.Grant)
	if (r.Grant.Grant.ParentEpoch != 0) != (r.RunID != "") {
		return r, guesttask.ErrConflict
	}
	if err != nil || r.Version != 1 || r.Handle != h || path != sshGuestPath(d.Root, h) || r.ProviderNodeID == "" || r.Grant.Grant.AgentID != d.AgentID || r.Result.Handle != h || r.Reported && r.Receipt == nil {
		return r, guesttask.ErrConflict
	}
	// Historical signatures prove identity, not permission for a new effect.
	if err = controlplane.VerifyExecutionGrant(d.IssuerKey, r.Grant, h.BootID, r.Grant.IssuedAt); err != nil {
		return r, err
	}
	if r.Receipt != nil {
		if r.Receipt.SSHStop != nil {
			if r.RunID == "" || controlplane.VerifyGuestSSHUnknownStopReceipt(d.HostKey.Public().(ed25519.PublicKey), *r.Receipt, r.Request, r.Ref.Node) != nil || r.Receipt.Grant != r.Grant.Grant || r.Result.Handle != r.Handle || r.Result.Present {
				return r, guesttask.ErrConflict
			}
		} else if controlplane.VerifyGuestSSHAccessStopReceipt(d.HostKey.Public().(ed25519.PublicKey), *r.Receipt, r.Request, r.Ref.Node) != nil || r.Receipt.Grant != r.Grant.Grant || r.Receipt.SSHResult == nil || *r.Receipt.SSHResult != r.Result {
			return r, guesttask.ErrConflict
		}
	}
	return r, nil
}
func sshOperationContext(ctx context.Context, r sshGuestRecord) context.Context {
	return substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: r.Request.ID, OwnerUID: r.Ref.Scope.TopologyUID, TopologyUID: r.Ref.Scope.TopologyUID, ResourceUID: r.Ref.Scope.ResourceUID, Generation: r.Ref.Scope.Generation})
}
func (d *SSHGuestDispatcher) Mutate(ctx context.Context, ref TargetRef, req substrate.SSHAccessMutation, g controlplane.SignedExecutionGrant) (substrate.SSHAccessResult, error) {
	var zero substrate.SSHAccessResult
	if err := d.configured(); err != nil {
		return zero, err
	}
	h, err := sshResultHandle(ref, req, g)
	if err != nil {
		return zero, err
	}
	zero.Handle = h
	if g.Grant.AgentID != d.AgentID || (g.Grant.ParentEpoch != 0) != (d.RunID != "") || (d.RunID != "" && d.ReportDerived == nil) || (d.RunID == "" && d.Report == nil) {
		return zero, effectgate.ErrFenced
	}
	if err = controlplane.VerifyExecutionGrant(d.IssuerKey, g, g.BootID, time.Now()); err != nil {
		return zero, err
	}
	budget, err := controlplane.ExecutionGrantBudget(g, time.Now())
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	unlock, err := d.lock(ctx, sshExecutionHandle(h))
	if err != nil {
		return zero, err
	}
	defer unlock()
	prior, err := d.loadSSH(sshGuestPath(d.Root, h))
	if err == nil {
		if prior.RunID != d.RunID || prior.Ref != ref || prior.Handle != h || !sameTaskGrant(prior.Grant.Grant, g.Grant) {
			return zero, guesttask.ErrConflict
		}
		return d.observeSSH(ctx, &prior)
	}
	if !os.IsNotExist(err) {
		return zero, err
	}
	if d.Authorize == nil {
		return zero, effectgate.ErrFenced
	}
	if err = d.Authorize(ctx, g); err != nil {
		return zero, err
	}
	target, err := d.targetRecord(ctx, TaskRecord{RunID: d.RunID, Ref: ref, Grant: g})
	if err != nil {
		return zero, err
	}
	if target.User != req.Config.User {
		return zero, guesttask.ErrConflict
	}
	access, ok := target.Connection.(substrate.ConnectionSSHAccess)
	if !ok {
		return zero, substrate.ErrNotSupported
	}
	r := sshGuestRecord{Version: 1, RunID: d.RunID, Ref: ref, Request: req, Handle: h, ProviderNodeID: target.NodeID, Grant: g, Result: zero}
	// A crash after saving intent is uncertain; recovery only observes evidence.
	if err = d.saveSSH(r); err != nil {
		return zero, err
	}
	if err = d.Authorize(ctx, g); err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	result, err := access.MutateSSHAccess(transport.WithTaskGrant(sshOperationContext(ctx, r), req.ID, g), req)
	if err != nil {
		return zero, err
	}
	return d.completeSSH(ctx, &r, result)
}

// Observe accepts an expired signed identity only if it matches durable intent.
// It never issues a mutation or interprets absent evidence as successful delete.
func (d *SSHGuestDispatcher) Observe(ctx context.Context, ref TargetRef, req substrate.SSHAccessMutation, g controlplane.SignedExecutionGrant) (substrate.SSHAccessResult, error) {
	var zero substrate.SSHAccessResult
	if err := d.configured(); err != nil {
		return zero, err
	}
	h, err := sshResultHandle(ref, req, g)
	if err != nil {
		return zero, err
	}
	if err = controlplane.VerifyExecutionGrant(d.IssuerKey, g, g.BootID, g.IssuedAt); err != nil {
		return zero, err
	}
	unlock, err := d.lock(ctx, sshExecutionHandle(h))
	if err != nil {
		return zero, err
	}
	defer unlock()
	r, err := d.loadSSH(sshGuestPath(d.Root, h))
	if err != nil {
		return zero, err
	}
	if r.Ref != ref || r.Handle != h || !sameTaskGrant(r.Grant.Grant, g.Grant) {
		return zero, guesttask.ErrConflict
	}
	return d.observeSSH(ctx, &r)
}
func (d *SSHGuestDispatcher) observeSSH(ctx context.Context, r *sshGuestRecord) (substrate.SSHAccessResult, error) {
	if r.Receipt != nil {
		if err := d.reportSSH(ctx, r); err != nil {
			return r.Result, err
		}
		if r.Receipt.SSHStop != nil {
			return r.Result, guesttask.ErrUnknown
		}
		return r.Result, nil
	}
	target, err := d.targetRecord(ctx, TaskRecord{RunID: r.RunID, Ref: r.Ref, Grant: r.Grant})
	if err != nil {
		return r.Result, err
	}
	if target.NodeID != r.ProviderNodeID || target.User != r.Request.Config.User {
		return r.Result, guesttask.ErrConflict
	}
	access, ok := target.Connection.(substrate.ConnectionSSHAccess)
	if !ok {
		return r.Result, substrate.ErrNotSupported
	}
	h := r.Handle
	h.NodeID = r.ProviderNodeID
	result, err := access.ObserveSSHAccess(sshOperationContext(ctx, *r), h)
	if err != nil {
		// Only an obsolete derived request can be retired by this path. Active
		// requests keep running; unavailable issuer is never permission to replay.
		stopper, ok := target.Connection.(substrate.ConnectionSSHStopper)
		if r.RunID == "" || !ok || d.Authorize == nil || !errors.Is(d.Authorize(ctx, r.Grant), ErrGrantObsolete) {
			return r.Result, err
		}
		stopped, stopErr := stopper.StopSSHAccess(transport.WithTaskGrant(sshOperationContext(ctx, *r), r.Request.ID, r.Grant), r.Request)
		if stopErr != nil {
			return r.Result, stopErr
		}
		if stopped.Handle != h || stopped.Validate(r.Grant.Grant.Epoch, r.Handle.BootID, r.Request) != nil {
			return r.Result, guesttask.ErrConflict
		}
		// Revalidate canonical identity after the potentially slow guest lock.
		current, resolveErr := d.targetRecord(ctx, TaskRecord{RunID: r.RunID, Ref: r.Ref, Grant: r.Grant})
		if resolveErr != nil {
			return r.Result, resolveErr
		}
		if current.NodeID != r.ProviderNodeID || current.User != r.Request.Config.User {
			return r.Result, guesttask.ErrConflict
		}
		if stopped.Outcome == "committed" {
			return d.completeSSH(ctx, r, *stopped.Result)
		}
		stopped.Handle.NodeID = r.Ref.Node
		receipt, signErr := controlplane.SignGuestSSHUnknownStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, r.Request, stopped, rand.Text(), time.Now())
		if signErr != nil {
			return r.Result, signErr
		}
		r.Result = substrate.SSHAccessResult{Handle: r.Handle}
		r.Receipt = &receipt
		if saveErr := d.saveSSH(*r); saveErr != nil {
			return r.Result, saveErr
		}
		if reportErr := d.reportSSH(ctx, r); reportErr != nil {
			return r.Result, reportErr
		}
		return r.Result, guesttask.ErrUnknown
	}
	return d.completeSSH(ctx, r, result)
}
func (d *SSHGuestDispatcher) completeSSH(ctx context.Context, r *sshGuestRecord, result substrate.SSHAccessResult) (substrate.SSHAccessResult, error) {
	expected := r.Handle
	expected.NodeID = r.ProviderNodeID
	if result.Handle != expected || result.Present == r.Request.Delete {
		return r.Result, guesttask.ErrConflict
	}
	result.Handle.NodeID = r.Ref.Node
	receipt, err := controlplane.SignGuestSSHAccessStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, r.Request, result, rand.Text(), time.Now())
	if err != nil {
		return r.Result, err
	}
	r.Result = result
	r.Receipt = &receipt
	if err = d.saveSSH(*r); err != nil {
		return result, err
	}
	return result, d.reportSSH(ctx, r)
}
func (d *SSHGuestDispatcher) reportSSH(ctx context.Context, r *sshGuestRecord) error {
	if r.Reported {
		return nil
	}
	raw, err := json.Marshal(r.Receipt)
	if err != nil {
		return err
	}
	if r.RunID != "" {
		if d.ReportDerived == nil {
			return effectgate.ErrFenced
		}
		err = d.ReportDerived(ctx, r.Request, *r.Receipt)
	} else {
		if d.Report == nil {
			return effectgate.ErrFenced
		}
		err = d.Report(ctx, r.Request.ID, *r.Receipt, raw)
	}
	if err != nil {
		return err
	}
	r.Reported = true
	return d.saveSSH(*r)
}
func (d *SSHGuestDispatcher) Recover(ctx context.Context) error {
	if err := d.configured(); err != nil {
		return err
	}
	if err := EnsureOwnedDirectory(d.Root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(d.Root)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		r, err := d.loadSSH(filepath.Join(d.Root, entry.Name()))
		if err == nil {
			operation, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
			_, err = d.Observe(operation, r.Ref, r.Request, r.Grant)
			if errors.Is(err, guesttask.ErrUnknown) {
				saved, loadErr := d.loadSSH(filepath.Join(d.Root, entry.Name()))
				if loadErr == nil && saved.Reported && saved.Receipt != nil && saved.Receipt.SSHStop != nil {
					err = nil
				}
			}
			cancel()
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
