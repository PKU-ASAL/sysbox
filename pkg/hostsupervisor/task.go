// Package hostsupervisor is the credential-owning task boundary. Workers send
// signed requests, never provider connection endpoints or private signing keys.
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
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

type Target struct {
	Scope           controlplane.ExecutionScope
	AgentID, NodeID string
	// User is the canonical guest account from the provider handle. It is
	// retained inside the supervisor boundary for operations such as managed
	// SSH access; workers never supply it.
	User       string
	Connection substrate.Connection
}
type Dispatcher struct {
	Root, AgentID, HostBootID string
	// RunID is set only by the trusted parent Run constructor.
	RunID          string
	IssuerKey      ed25519.PublicKey
	HostKey        ed25519.PrivateKey
	Resolve        func(context.Context, TargetRef) (Target, error)
	ResolveRun     func(context.Context, TargetRef, string) (Target, error)
	FindRunTaskRef func(context.Context, string, string, controlplane.ExecutionScope) (TargetRef, error)
	Authorize      func(context.Context, controlplane.SignedExecutionGrant) error
	ReportStop     func(context.Context, controlplane.SignedExecutionStopReceipt) error
	PendingStops   func(context.Context, controlplane.GuestTaskStopCursor) (controlplane.GuestTaskStopPage, error)
}
type TaskRecord struct {
	Cancellation    *controlplane.SignedExecutionCancellation `json:"cancellation,omitempty"`
	RunID           string                                    `json:"run_id,omitempty"`
	Ref             TargetRef                                 `json:"ref"`
	ProviderNodeID  string                                    `json:"provider_node_id"`
	Version         int                                       `json:"version"`
	Handle          substrate.ExecutionHandle                 `json:"handle"`
	Grant           controlplane.SignedExecutionGrant         `json:"grant"`
	Result          substrate.ExecResult                      `json:"result"`
	CancelRequested bool                                      `json:"cancel_requested"`
	StopReceipt     *controlplane.SignedExecutionStopReceipt  `json:"stop_receipt,omitempty"`
	Reported        bool                                      `json:"reported"`
}

// Each finite recovery pass visits every record. Reserve time after authority
// timeout for owned cancellation; guest-side expiry remains independent.
const recoveryTaskBudget = 2 * time.Second
const recoveryAuthorizationBudget = 750 * time.Millisecond

func recordKey(h substrate.ExecutionHandle) string {
	raw, _ := json.Marshal([]any{h.TopologyUID, h.ResourceUID, h.Generation, h.Epoch, h.ID, h.BootID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (d *Dispatcher) path(h substrate.ExecutionHandle) string {
	return filepath.Join(d.Root, recordKey(h)+".json")
}
func (d *Dispatcher) lock(ctx context.Context, h substrate.ExecutionHandle) (func(), error) {
	if d.Root == "" || d.AgentID == "" || d.HostBootID == "" || len(d.HostKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("host task supervisor not configured")
	}
	if err := EnsureOwnedDirectory(d.Root, 0700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(d.Root, recordKey(h)+".lock")
	if _, err := os.Lstat(lockPath); err == nil {
		if err := ValidateOwnedPath(lockPath, false); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	l := flock.New(lockPath)
	ok, err := l.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !ok {
		l.Close()
		if err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	return func() { l.Close() }, nil
}
func (d *Dispatcher) load(h substrate.ExecutionHandle) (TaskRecord, error) {
	var r TaskRecord
	if err := ValidateOwnedPath(d.path(h), true); err != nil {
		return r, err
	}
	raw, err := os.ReadFile(d.path(h))
	if err != nil {
		return r, err
	}
	if len(raw) > 4<<20 {
		return r, fmt.Errorf("oversized supervisor task record")
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Version != 3 || r.Ref.Topology == "" || r.Ref.Node != h.NodeID || r.Ref.Scope != r.Grant.Grant.Scope || !sameHandle(r.Handle, h) || r.Grant.Grant.AgentID != d.AgentID || r.Grant.Grant.Scope.TopologyUID != h.TopologyUID || r.Grant.Grant.Scope.ResourceUID != h.ResourceUID || r.Grant.Grant.Scope.Generation != h.Generation || r.Grant.Grant.Epoch != h.Epoch || r.Grant.Grant.ActionID != h.ID || r.Grant.Grant.Digest != h.Digest || r.Grant.BootID != h.BootID {
		return r, guesttask.ErrConflict
	}
	if (r.Grant.Grant.ParentEpoch != 0) != (r.RunID != "") {
		return r, guesttask.ErrConflict
	}
	// Historical signatures remain verifiable after expiry; this is record
	// integrity validation, not authorization to execute or renew the grant.
	if r.Cancellation != nil && (controlplane.VerifyExecutionCancellation(d.IssuerKey, *r.Cancellation) != nil || !sameTaskGrant(r.Grant.Grant, r.Cancellation.Grant) || !r.CancelRequested) {
		return r, guesttask.ErrConflict
	}
	if r.Cancellation != nil && (r.Cancellation.RunID != r.RunID || (r.RunID != "" && r.Cancellation.Topology != r.Ref.Topology)) {
		return r, guesttask.ErrConflict
	}
	if len(r.Grant.Signature) != 0 {
		if controlplane.VerifyExecutionGrant(d.IssuerKey, r.Grant, h.BootID, r.Grant.IssuedAt) != nil {
			return r, guesttask.ErrConflict
		}
	} else if r.Cancellation == nil || !r.Grant.IssuedAt.IsZero() || r.Grant.Grant != r.Cancellation.Grant {
		return r, guesttask.ErrConflict
	}
	if r.ProviderNodeID == "" && (r.Cancellation == nil || len(r.Grant.Signature) != 0 || r.StopReceipt != nil) {
		return r, guesttask.ErrConflict
	}
	if r.StopReceipt != nil && (controlplane.VerifyExecutionStopReceipt(d.HostKey.Public().(ed25519.PublicKey), *r.StopReceipt) != nil || r.StopReceipt.GuestBootID != h.BootID || r.StopReceipt.TaskID != h.ID || r.StopReceipt.TaskDigest != h.Digest || r.StopReceipt.Grant != r.Grant.Grant) {
		return r, guesttask.ErrConflict
	}
	if r.Reported && r.StopReceipt == nil {
		return r, guesttask.ErrConflict
	}
	if r.StopReceipt != nil {
		observed, err := controlplane.NewGuestTaskResult(r.Result)
		if err != nil || observed.ExitCodeKnown != r.StopReceipt.GuestResult.ExitCodeKnown || observed.ResultClass != r.StopReceipt.GuestResult.ResultClass || observed.Result.ExitCode != r.StopReceipt.GuestResult.Result.ExitCode {
			return r, guesttask.ErrConflict
		}
	}
	return r, nil
}
func sameHandle(a, b substrate.ExecutionHandle) bool {
	a.PID = 0
	b.PID = 0
	return reflect.DeepEqual(a, b)
}
func (d *Dispatcher) save(r TaskRecord) error {
	return saveRecord(d.Root, d.path(r.Handle), r)
}
func saveRecord(root, path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (d *Dispatcher) target(ctx context.Context, ref TargetRef, g controlplane.ExecutionGrant) (Target, error) {
	if ref.Topology == "" || ref.Node == "" || ref.Scope != g.Scope {
		return Target{}, guesttask.ErrConflict
	}
	if d.Resolve == nil {
		return Target{}, fmt.Errorf("canonical resource resolver unavailable")
	}
	target, err := d.Resolve(ctx, ref)
	if err != nil {
		return target, err
	}
	if g.AgentID != d.AgentID || target.AgentID != d.AgentID || target.Scope != g.Scope || target.NodeID == "" || target.Connection == nil {
		return Target{}, guesttask.ErrConflict
	}
	return target, nil
}

func (d *Dispatcher) targetRecord(ctx context.Context, r TaskRecord) (Target, error) {
	if (r.Grant.Grant.ParentEpoch != 0) != (r.RunID != "") {
		return Target{}, guesttask.ErrConflict
	}
	if r.RunID == "" {
		return d.target(ctx, r.Ref, r.Grant.Grant)
	}
	if d.ResolveRun == nil {
		return Target{}, fmt.Errorf("parent Run resource resolver unavailable")
	}
	scoped := *d
	scoped.Resolve = func(ctx context.Context, ref TargetRef) (Target, error) {
		return d.ResolveRun(ctx, ref, r.RunID)
	}
	return scoped.target(ctx, r.Ref, r.Grant.Grant)
}
func (d *Dispatcher) BootID(ctx context.Context, ref TargetRef) (string, error) {
	target, err := d.target(ctx, ref, controlplane.ExecutionGrant{Scope: ref.Scope, AgentID: d.AgentID})
	if err != nil {
		return "", err
	}
	info, ok := target.Connection.(interface {
		GuestTaskBootID(context.Context) (string, error)
	})
	if !ok {
		return "", substrate.ErrNotSupported
	}
	return info.GuestTaskBootID(ctx)
}
func (d *Dispatcher) ReadOutput(ctx context.Context, ref TargetRef, h substrate.ExecutionHandle, stream string, cursor int64, limit int) (substrate.ExecutionOutput, error) {
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return substrate.ExecutionOutput{}, err
	}
	defer unlock()
	r, err := d.load(h)
	if err != nil {
		return substrate.ExecutionOutput{}, err
	}
	if r.Ref != ref {
		return substrate.ExecutionOutput{}, guesttask.ErrConflict
	}
	target, err := d.targetRecord(ctx, r)
	if err != nil {
		return substrate.ExecutionOutput{}, err
	}
	if target.NodeID != r.ProviderNodeID {
		return substrate.ExecutionOutput{}, guesttask.ErrConflict
	}
	reader, ok := target.Connection.(substrate.ConnectionExecutionOutput)
	if !ok {
		return substrate.ExecutionOutput{}, substrate.ErrNotSupported
	}
	h.NodeID = target.NodeID
	return reader.ReadExecutionOutput(ctx, h, stream, cursor, limit)
}
func (d *Dispatcher) Start(ctx context.Context, ref TargetRef, req guesttask.Request) (TaskRecord, error) {
	var zero TaskRecord
	if req.UserID != 65534 {
		return zero, fmt.Errorf("host task policy permits only reserved uid65534")
	}
	if err := req.Validate(); err != nil {
		return zero, err
	}
	g := req.Grant
	if (g.Grant.ParentEpoch != 0) != (d.RunID != "") {
		return zero, guesttask.ErrUnauthorized
	}
	if err := controlplane.VerifyExecutionGrant(d.IssuerKey, g, g.BootID, time.Now()); err != nil {
		return zero, err
	}
	digest, err := req.Digest()
	if err != nil {
		return zero, err
	}
	if g.Grant.Operation != "guest_task" || g.Grant.Digest != digest || g.Grant.ActionID != req.ID || g.Grant.Scope.ResourceUID == "" {
		return zero, guesttask.ErrUnauthorized
	}
	if d.Authorize == nil {
		return zero, fmt.Errorf("execution grant authority unavailable")
	}
	if err = d.Authorize(ctx, g); err != nil {
		return zero, err
	}
	target, err := d.target(ctx, ref, g.Grant)
	if err != nil {
		return zero, err
	}
	h := substrate.ExecutionHandle{ID: req.ID, Digest: digest, BootID: g.BootID, Epoch: g.Grant.Epoch, NodeID: ref.Node, TopologyUID: g.Grant.Scope.TopologyUID, ResourceUID: g.Grant.Scope.ResourceUID, Generation: g.Grant.Scope.Generation}
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return zero, err
	}
	defer unlock()
	old, err := d.load(h)
	if err == nil {
		if old.RunID != d.RunID {
			return zero, guesttask.ErrConflict
		}
		return old, nil
	}
	if !os.IsNotExist(err) {
		return zero, err
	}
	r := TaskRecord{Version: 3, RunID: d.RunID, Ref: ref, ProviderNodeID: target.NodeID, Handle: h, Grant: g, Result: substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationUnknown}}
	if err = d.save(r); err != nil {
		return r, err
	}
	starter, ok := target.Connection.(substrate.ConnectionStarter)
	if !ok {
		return r, substrate.ErrNotSupported
	}
	request := substrate.ExecRequest{Program: req.Args[0], Args: req.Args[1:], Environment: req.Environment, WorkingDir: req.WorkingDir, Shell: substrate.ShellNone, Stdin: bytes.NewReader(req.Input), OutputLimitBytes: req.OutputLimit}
	startTime := time.Now()
	handle, err := starter.Start(transport.WithTaskGrant(ctx, req.ID, g), request)
	if elapsed := time.Since(startTime); elapsed > recoveryTaskBudget {
		slog.Warn("slow guest task start acknowledgement", "task", req.ID, "generation", h.Generation, "elapsed", elapsed, "failed", err != nil)
	}
	if err != nil {
		return r, err
	}
	if handle.NodeID != target.NodeID {
		return r, guesttask.ErrConflict
	}
	handle.NodeID = ref.Node
	if !sameHandle(h, handle) {
		return r, guesttask.ErrConflict
	}
	r.Handle = handle
	return r, d.save(r)
}
func (d *Dispatcher) Observe(ctx context.Context, ref TargetRef, h substrate.ExecutionHandle) (TaskRecord, error) {
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return TaskRecord{}, err
	}
	defer unlock()
	r, err := d.load(h)
	if err != nil {
		return r, err
	}
	if r.Ref != ref {
		return r, guesttask.ErrConflict
	}
	return d.observeLocked(ctx, r)
}
func (d *Dispatcher) observeLocked(ctx context.Context, r TaskRecord) (TaskRecord, error) {
	if r.StopReceipt != nil {
		return d.report(ctx, r)
	}
	target, err := d.targetRecord(ctx, r)
	if err != nil {
		return r, err
	}
	if target.NodeID != r.ProviderNodeID {
		return r, guesttask.ErrConflict
	}
	observer, ok := target.Connection.(substrate.ConnectionExecutionObserver)
	if !ok {
		return r, substrate.ErrNotSupported
	}
	providerHandle := r.Handle
	providerHandle.NodeID = target.NodeID
	result, err := observer.ObserveExecution(ctx, providerHandle)
	if err != nil {
		return r, err
	}
	r.Result = result
	if result.RemoteTermination == substrate.RemoteTerminationConfirmed {
		reader, ok := target.Connection.(substrate.ConnectionExecutionOutput)
		if !ok {
			return r, substrate.ErrNotSupported
		}
		var truncated bool
		result.Stdout, truncated, err = readTaskResultOutput(ctx, reader, providerHandle, "stdout")
		result.StdoutTruncated = result.StdoutTruncated || truncated
		if err != nil {
			return r, err
		}
		result.Stderr, truncated, err = readTaskResultOutput(ctx, reader, providerHandle, "stderr")
		result.StderrTruncated = result.StderrTruncated || truncated
		if err != nil {
			return r, err
		}
		guest, err := controlplane.NewGuestTaskResult(result)
		if err != nil {
			return r, err
		}
		receipt, err := controlplane.SignExecutionTaskStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, r.Handle.BootID, r.Handle.ID, r.Handle.Digest, guest, rand.Text(), time.Now())
		if err != nil {
			return r, err
		}
		r.StopReceipt = &receipt
		r.Result.StdoutTruncated, r.Result.StderrTruncated = result.StdoutTruncated, result.StderrTruncated
		// Output is retained once, in the signed receipt. Workers use the
		// durable output API; avoid duplicating binary data in the task record.
		r.Result.Stdout, r.Result.Stderr = "", ""
	}
	if err = d.save(r); err != nil {
		return r, err
	}
	return d.report(ctx, r)
}
func (d *Dispatcher) report(ctx context.Context, r TaskRecord) (TaskRecord, error) {
	if r.StopReceipt == nil || r.Reported {
		return r, nil
	}
	if d.ReportStop == nil {
		return r, fmt.Errorf("stop receipt reporting unavailable")
	}
	if err := d.ReportStop(ctx, *r.StopReceipt); err != nil {
		return r, err
	}
	r.Reported = true
	return r, d.save(r)
}
func (d *Dispatcher) Cancel(ctx context.Context, ref TargetRef, h substrate.ExecutionHandle) (TaskRecord, error) {
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return TaskRecord{}, err
	}
	defer unlock()
	r, err := d.load(h)
	if err != nil {
		return r, err
	}
	if r.Ref != ref {
		return r, guesttask.ErrConflict
	}
	return d.cancelLocked(ctx, r)
}
func (d *Dispatcher) cancelLocked(ctx context.Context, r TaskRecord) (TaskRecord, error) {
	if r.StopReceipt != nil {
		return d.report(ctx, r)
	}
	target, err := d.targetRecord(ctx, r)
	if err != nil {
		return r, err
	}
	if r.ProviderNodeID == "" && r.Cancellation != nil {
		r.ProviderNodeID = target.NodeID
	}
	if target.NodeID != r.ProviderNodeID {
		return r, guesttask.ErrConflict
	}
	canceller, ok := target.Connection.(substrate.ConnectionExecutionCanceler)
	if !ok {
		return r, substrate.ErrNotSupported
	}
	r.CancelRequested = true
	if err = d.save(r); err != nil {
		return r, err
	}
	if r.Cancellation != nil {
		if info, ok := target.Connection.(interface {
			GuestTaskBootID(context.Context) (string, error)
		}); ok {
			boot, err := info.GuestTaskBootID(ctx)
			if err != nil {
				return r, err
			}
			if boot == "" {
				return r, guesttask.ErrUnknown
			}
			if boot != r.Handle.BootID {
				receipt, err := controlplane.SignRebootedTaskStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, boot, rand.Text(), time.Now())
				if err != nil {
					return r, err
				}
				r.Result = substrate.ExecResult{Status: substrate.ExecStatusUnknown, RemoteTermination: substrate.RemoteTerminationConfirmed}
				r.StopReceipt = &receipt
				if err = d.save(r); err != nil {
					return r, err
				}
				return d.report(ctx, r)
			}
		}
	}
	providerHandle := r.Handle
	providerHandle.NodeID = target.NodeID
	if r.Cancellation != nil {
		ctx = transport.WithTaskCancellation(ctx, *r.Cancellation)
	}
	if _, err = canceller.CancelExecution(ctx, providerHandle); err != nil {
		return r, err
	}
	return d.observeLocked(ctx, r)
}
func (d *Dispatcher) Renew(ctx context.Context, ref TargetRef, h substrate.ExecutionHandle, g controlplane.SignedExecutionGrant) (TaskRecord, error) {
	if err := controlplane.VerifyExecutionGrant(d.IssuerKey, g, h.BootID, time.Now()); err != nil {
		return TaskRecord{}, err
	}
	if d.Authorize == nil {
		return TaskRecord{}, guesttask.ErrUnauthorized
	}
	if err := d.Authorize(ctx, g); err != nil {
		return TaskRecord{}, err
	}
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return TaskRecord{}, err
	}
	defer unlock()
	r, err := d.load(h)
	if err != nil {
		return r, err
	}
	old, next := r.Grant.Grant, g.Grant
	old.ExpiresAt = next.ExpiresAt
	if r.Ref != ref || old != next || r.CancelRequested || r.StopReceipt != nil || g.Grant.ExpiresAt.Before(r.Grant.Grant.ExpiresAt) {
		return r, guesttask.ErrConflict
	}
	target, err := d.targetRecord(ctx, r)
	if err != nil {
		return r, err
	}
	if target.NodeID != r.ProviderNodeID {
		return r, guesttask.ErrConflict
	}
	renewer, ok := target.Connection.(substrate.ConnectionExecutionRenewer)
	if !ok {
		return r, substrate.ErrNotSupported
	}
	// Persist new grant before forwarding; recovery must never retain a shorter
	// deadline than one which may have reached the guest.
	r.Grant = g
	if err = d.save(r); err != nil {
		return r, err
	}
	providerHandle := h
	providerHandle.NodeID = target.NodeID
	err = renewer.RenewExecution(transport.WithTaskGrant(ctx, h.ID, g), providerHandle)
	return r, err
}
func (d *Dispatcher) Recover(ctx context.Context) error {
	entries, err := os.ReadDir(d.Root)
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return err
	}
	var failures []error
	var pending, active []substrate.ExecutionHandle
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var record TaskRecord
		raw, err := os.ReadFile(filepath.Join(d.Root, entry.Name()))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			failures = append(failures, err)
			continue
		}
		if recordKey(record.Handle)+".json" != entry.Name() {
			failures = append(failures, guesttask.ErrConflict)
			continue
		}
		if record.StopReceipt != nil {
			pending = append(pending, record.Handle)
		} else {
			active = append(active, record.Handle)
		}
	}
	// Each record has its own budget; the caller must not impose a short
	// whole-pass deadline. Pending receipts are local evidence and go first.
	for _, handle := range append(pending, active...) {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		taskCtx, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
		err := d.recoverTask(taskCtx, handle)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("task %s generation %d: %w", handle.ID, handle.Generation, err))
		}
	}
	if err := d.recoverPendingStops(ctx); err != nil {
		failures = append(failures, fmt.Errorf("pending stops: %w", err))
	}
	return errors.Join(failures...)
}

func (d *Dispatcher) recoverTask(ctx context.Context, h substrate.ExecutionHandle) error {
	unlock, err := d.lock(ctx, h)
	if err != nil {
		return fmt.Errorf("acquire task lock: %w", err)
	}
	snapshot, err := d.load(h)
	if err != nil {
		unlock()
		return err
	}
	if snapshot.StopReceipt != nil {
		_, err = d.report(ctx, snapshot)
		unlock()
		return err
	}
	if snapshot.CancelRequested {
		_, err = d.cancelLocked(ctx, snapshot)
		unlock()
		return err
	}
	unlock()
	// Avoid holding the task lock during online authorization so renewal can
	// complete. Reacquire and compare the exact grant before acting on its reply.
	authorityErr := guesttask.ErrUnauthorized
	if d.Authorize != nil {
		authCtx, cancel := context.WithTimeout(ctx, recoveryAuthorizationBudget)
		authorityErr = d.Authorize(authCtx, snapshot.Grant)
		cancel()
	}
	unlock, err = d.lock(ctx, h)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := d.load(h)
	if err != nil {
		return err
	}
	if current.StopReceipt != nil {
		_, err = d.report(ctx, current)
		return err
	}
	if !reflect.DeepEqual(current.Grant, snapshot.Grant) {
		return nil
	}
	if current.CancelRequested || authorityErr != nil || !current.Grant.Grant.ExpiresAt.After(time.Now()) {
		_, err = d.cancelLocked(ctx, current)
	} else {
		_, err = d.observeLocked(ctx, current)
	}
	return err
}
