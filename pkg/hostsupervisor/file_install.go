package hostsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// FileDispatcher shares the host identity, canonical resolver and durable
// journal storage with task supervision, with a separate journal directory.
// The only payload source is the configured authority, never a worker URL.
type FileDispatcher struct {
	*Dispatcher
	Fetch func(context.Context, string) (io.ReadCloser, error)
}
type FileRecord struct {
	Version         int                                      `json:"version"`
	Ref             TargetRef                                `json:"ref"`
	Request         substrate.FileInstallRequest             `json:"request"`
	Handle          substrate.FileInstallHandle              `json:"handle"`
	ProviderNodeID  string                                   `json:"provider_node_id"`
	Grant           controlplane.SignedExecutionGrant        `json:"grant"`
	Result          substrate.FileInstallResult              `json:"result"`
	CancelRequested bool                                     `json:"cancel_requested"`
	Dispatched      bool                                     `json:"dispatched"`
	Receipt         *controlplane.SignedExecutionStopReceipt `json:"receipt,omitempty"`
	Reported        bool                                     `json:"reported"`
}

func fileExecutionHandle(h substrate.FileInstallHandle) substrate.ExecutionHandle {
	return substrate.ExecutionHandle{ID: h.ID, Digest: h.Digest, BootID: h.BootID, NodeID: h.NodeID, TopologyUID: h.TopologyUID, ResourceUID: h.ResourceUID, Generation: h.Generation, Epoch: h.Epoch}
}
func (d *FileDispatcher) loadFile(h substrate.FileInstallHandle) (FileRecord, error) {
	var r FileRecord
	path := d.path(fileExecutionHandle(h))
	if err := ValidateOwnedPath(path, true); err != nil {
		return r, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if len(raw) > 1<<20 {
		return r, guesttask.ErrConflict
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	g := r.Grant.Grant
	digest, err := r.Request.Digest()
	if err != nil || r.Version != 1 || r.Handle != h || r.Ref.Node != h.NodeID || r.Ref.Scope != g.Scope || r.Dispatched && r.ProviderNodeID == "" || r.Request.ID != h.ID || digest != h.Digest || digest != g.Digest || h.ID != g.ActionID || g.AgentID != d.AgentID || g.Operation != "guest_file_install" || g.Scope.TopologyUID != h.TopologyUID || g.Scope.ResourceUID != h.ResourceUID || g.Scope.Generation != h.Generation || g.Epoch != h.Epoch || g.TargetBootID != h.BootID || r.Grant.BootID != h.BootID {
		return r, guesttask.ErrConflict
	}
	if controlplane.VerifyExecutionGrant(d.IssuerKey, r.Grant, h.BootID, r.Grant.IssuedAt) != nil {
		return r, guesttask.ErrUnauthorized
	}
	if r.Receipt != nil && (controlplane.VerifyExecutionStopReceipt(d.HostKey.Public().(ed25519.PublicKey), *r.Receipt) != nil || r.Receipt.Grant != g || r.Receipt.InstallID != h.ID || r.Receipt.InstallDigest != h.Digest || r.Receipt.FileResult == nil || *r.Receipt.FileResult != r.Result) {
		return r, guesttask.ErrConflict
	}
	if r.Reported && r.Receipt == nil {
		return r, guesttask.ErrConflict
	}
	return r, nil
}
func (d *FileDispatcher) saveFile(r FileRecord) error {
	return saveRecord(d.Root, d.path(fileExecutionHandle(r.Handle)), r)
}
func (d *FileDispatcher) BootID(ctx context.Context, ref TargetRef) (string, error) {
	t, err := d.target(ctx, ref, controlplane.ExecutionGrant{Scope: ref.Scope, AgentID: d.AgentID})
	if err != nil {
		return "", err
	}
	c, ok := t.Connection.(interface {
		GuestFileBootID(context.Context) (string, error)
	})
	if !ok {
		return "", substrate.ErrNotSupported
	}
	return c.GuestFileBootID(ctx)
}
func (d *FileDispatcher) Install(ctx context.Context, ref TargetRef, req substrate.FileInstallRequest, g controlplane.SignedExecutionGrant) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
	var zero substrate.FileInstallResult
	digest, err := req.Digest()
	if err != nil {
		return substrate.FileInstallHandle{}, zero, err
	}
	h := substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: g.BootID, NodeID: ref.Node, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: g.Grant.Epoch}
	if g.Grant.Operation != "guest_file_install" || g.Grant.Digest != digest || g.Grant.ActionID != req.ID || g.Grant.TargetBootID == "" || g.Grant.Scope != ref.Scope || g.Grant.AgentID != d.AgentID {
		return h, zero, guesttask.ErrUnauthorized
	}
	if err = controlplane.VerifyExecutionGrant(d.IssuerKey, g, g.BootID, time.Now()); err != nil {
		return h, zero, err
	}
	budget, err := controlplane.ExecutionGrantBudget(g, time.Now())
	if err != nil {
		return h, zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	unlock, err := d.lock(ctx, fileExecutionHandle(h))
	if err != nil {
		return h, zero, err
	}
	defer func() { unlock() }()
	old, err := d.loadFile(h)
	if err == nil {
		if old.Ref != ref || old.Request != req {
			return h, zero, guesttask.ErrConflict
		}
		return h, old.Result, d.reportFile(ctx, old)
	}
	if !os.IsNotExist(err) {
		return h, zero, err
	}
	r := FileRecord{Version: 1, Ref: ref, Request: req, Handle: h, Grant: g, Result: substrate.FileInstallResult{Outcome: substrate.FileInstallUnknown}}
	if err = d.saveFile(r); err != nil {
		return h, zero, err
	}
	abort := func(cause error) (substrate.FileInstallHandle, substrate.FileInstallResult, error) {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		stopErr := d.finishFile(cleanup, &r, substrate.FileInstallResult{Outcome: substrate.FileInstallCancelled, Quiescent: true})
		return h, r.Result, errors.Join(cause, stopErr)
	}
	if d.Authorize == nil || d.Fetch == nil {
		return abort(guesttask.ErrUnauthorized)
	}
	if err = d.Authorize(ctx, g); err != nil {
		return abort(err)
	}
	target, err := d.target(ctx, ref, g.Grant)
	if err != nil {
		return abort(err)
	}
	installer, ok := target.Connection.(substrate.ConnectionFileInstaller)
	if !ok {
		return abort(substrate.ErrNotSupported)
	}
	body, err := d.Fetch(ctx, req.ID)
	if err != nil {
		return abort(err)
	}
	data, err := io.ReadAll(io.LimitReader(body, substrate.MaxFileInstallSize+1))
	closeErr := body.Close()
	if err != nil {
		return abort(err)
	}
	if closeErr != nil {
		return abort(closeErr)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != req.Size || hex.EncodeToString(sum[:]) != req.SHA256 {
		return abort(fmt.Errorf("installation payload does not match signed digest and size"))
	}
	if err = ctx.Err(); err != nil {
		return abort(err)
	}
	if err = d.Authorize(ctx, g); err != nil {
		return abort(err)
	}
	r.ProviderNodeID = target.NodeID
	r.Dispatched = true
	if err = d.saveFile(r); err != nil {
		return h, zero, err
	}
	// The durable dispatched flag precedes the RPC, but the journal lock must
	// not prevent a concurrent cancel from reaching the guest's commit lock.
	unlock()
	unlock = func() {}
	providerHandle, result, callErr := installer.InstallFile(transport.WithTaskGrant(ctx, req.ID, g), req, bytes.NewReader(data))
	commitCtx, stopCommit := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCommit()
	release, err := d.lock(commitCtx, fileExecutionHandle(h))
	if err != nil {
		return h, r.Result, err
	}
	unlock = release
	r, err = d.loadFile(h)
	if err != nil {
		return h, zero, err
	}
	if r.Receipt != nil {
		return h, r.Result, d.reportFile(commitCtx, r)
	}
	if callErr != nil {
		return h, r.Result, callErr
	}
	if providerHandle.NodeID != target.NodeID {
		return h, r.Result, guesttask.ErrConflict
	}
	providerHandle.NodeID = ref.Node
	if providerHandle != h {
		return h, r.Result, guesttask.ErrConflict
	}
	err = d.finishFile(commitCtx, &r, result)
	return h, r.Result, err
}
func (d *FileDispatcher) finishFile(ctx context.Context, r *FileRecord, result substrate.FileInstallResult) error {
	if result.Quiescent {
		if result.Outcome != substrate.FileInstallCommitted && result.Outcome != substrate.FileInstallCancelled && !(result.Outcome == substrate.FileInstallUnknown && result.StoppedEpoch == r.Grant.Grant.Epoch) {
			return guesttask.ErrConflict
		}
		if (result.Outcome == substrate.FileInstallCommitted || result.StoppedEpoch != 0) && (result.SHA256 != r.Request.SHA256 || result.Size != r.Request.Size || result.Mode != r.Request.Mode) {
			return guesttask.ErrConflict
		}
		receipt, err := controlplane.SignExecutionFileStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, r.Handle.BootID, r.Handle.ID, r.Handle.Digest, result, rand.Text(), time.Now())
		if err != nil {
			return err
		}
		r.Receipt = &receipt
	} else if result.Outcome == substrate.FileInstallCommitted || result.Outcome == substrate.FileInstallCancelled {
		return guesttask.ErrConflict
	}
	r.Result = result
	if err := d.saveFile(*r); err != nil {
		return err
	}
	return d.reportFile(ctx, *r)
}
func (d *FileDispatcher) reportFile(ctx context.Context, r FileRecord) error {
	if r.Receipt == nil || r.Reported {
		return nil
	}
	if d.ReportStop == nil {
		return fmt.Errorf("file stop reporting unavailable")
	}
	if err := d.ReportStop(ctx, *r.Receipt); err != nil {
		return err
	}
	r.Reported = true
	return d.saveFile(r)
}
func (d *FileDispatcher) Observe(ctx context.Context, ref TargetRef, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return d.observeFile(ctx, ref, h, false)
}
func (d *FileDispatcher) Cancel(ctx context.Context, ref TargetRef, h substrate.FileInstallHandle) (substrate.FileInstallResult, error) {
	return d.observeFile(ctx, ref, h, true)
}
func (d *FileDispatcher) observeFile(ctx context.Context, ref TargetRef, h substrate.FileInstallHandle, cancel bool) (substrate.FileInstallResult, error) {
	var zero substrate.FileInstallResult
	unlock, err := d.lock(ctx, fileExecutionHandle(h))
	if err != nil {
		return zero, err
	}
	defer func() { unlock() }()
	r, err := d.loadFile(h)
	if err != nil {
		return zero, err
	}
	if r.Ref != ref {
		return zero, guesttask.ErrConflict
	}
	if r.Receipt != nil {
		return r.Result, d.reportFile(ctx, r)
	}
	if !r.Dispatched {
		err = d.finishFile(ctx, &r, substrate.FileInstallResult{Outcome: substrate.FileInstallCancelled, Quiescent: true})
		return r.Result, err
	}
	target, err := d.target(ctx, ref, r.Grant.Grant)
	if err != nil {
		return r.Result, err
	}
	if target.NodeID != r.ProviderNodeID {
		return r.Result, guesttask.ErrConflict
	}
	if info, ok := target.Connection.(interface {
		GuestFileBootID(context.Context) (string, error)
	}); ok {
		boot, err := info.GuestFileBootID(ctx)
		if err != nil {
			return r.Result, err
		}
		if boot == "" {
			return r.Result, guesttask.ErrUnknown
		}
		if boot != h.BootID {
			if r.Request.RebootScope != "guest-tmpfs" {
				return r.Result, guesttask.ErrUnknown
			}
			current, err := d.target(ctx, ref, r.Grant.Grant)
			if err != nil {
				return r.Result, err
			}
			if current.NodeID != r.ProviderNodeID {
				return r.Result, guesttask.ErrConflict
			}
			receipt, err := controlplane.SignRebootedFileStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, boot, rand.Text(), time.Now())
			if err != nil {
				return r.Result, err
			}
			r.Result = *receipt.FileResult
			r.Receipt = &receipt
			if err = d.saveFile(r); err != nil {
				return r.Result, err
			}
			return r.Result, d.reportFile(ctx, r)
		}
	}
	installer, ok := target.Connection.(substrate.ConnectionFileInstaller)
	if !ok {
		return r.Result, substrate.ErrNotSupported
	}
	providerHandle := h
	providerHandle.NodeID = target.NodeID
	var result substrate.FileInstallResult
	if cancel || r.CancelRequested {
		r.CancelRequested = true
		if err = d.saveFile(r); err != nil {
			return r.Result, err
		}
		unlock()
		unlock = func() {}
		result, err = installer.CancelFileInstall(transport.WithTaskGrant(ctx, h.ID, r.Grant), providerHandle)
		if err != nil {
			if stopper, ok := target.Connection.(interface {
				StopFileInstall(context.Context, substrate.FileInstallHandle) (substrate.FileInstallResult, error)
			}); ok {
				current, checkErr := d.target(ctx, ref, r.Grant.Grant)
				if checkErr != nil {
					return r.Result, checkErr
				}
				if current.NodeID != r.ProviderNodeID {
					return r.Result, guesttask.ErrConflict
				}
				result, err = stopper.StopFileInstall(transport.WithTaskGrant(ctx, h.ID, r.Grant), providerHandle)
				if err == nil {
					current, checkErr = d.target(ctx, ref, r.Grant.Grant)
					if checkErr != nil {
						return r.Result, checkErr
					}
					if current.NodeID != r.ProviderNodeID {
						return r.Result, guesttask.ErrConflict
					}
				}
			}
		}
	} else {
		unlock()
		unlock = func() {}
		result, err = installer.ObserveFileInstall(ctx, providerHandle)
	}
	if err != nil {
		return r.Result, err
	}
	release, err := d.lock(ctx, fileExecutionHandle(h))
	if err != nil {
		return r.Result, err
	}
	unlock = release
	r, err = d.loadFile(h)
	if err != nil {
		return zero, err
	}
	if r.Receipt != nil {
		return r.Result, d.reportFile(ctx, r)
	}
	err = d.finishFile(ctx, &r, result)
	return r.Result, err
}
func (d *FileDispatcher) Recover(ctx context.Context) error {
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
		var r FileRecord
		raw, e := os.ReadFile(filepath.Join(d.Root, entry.Name()))
		if e == nil {
			e = json.Unmarshal(raw, &r)
		}
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if entry.Name() != recordKey(fileExecutionHandle(r.Handle))+".json" {
			failures = append(failures, guesttask.ErrConflict)
			continue
		}
		operation, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
		shouldCancel := r.CancelRequested
		if r.Receipt == nil {
			authCtx, stop := context.WithTimeout(operation, recoveryAuthorizationBudget)
			if d.Authorize == nil || d.Authorize(authCtx, r.Grant) != nil || !r.Grant.Grant.ExpiresAt.After(time.Now()) {
				shouldCancel = true
			}
			stop()
		}
		_, e = d.observeFile(operation, r.Ref, r.Handle, shouldCancel)
		cancel()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
