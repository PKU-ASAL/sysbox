package guesttask

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

type FileInstallRecord struct {
	Version        int                          `json:"version"`
	Request        substrate.FileInstallRequest `json:"request"`
	Handle         substrate.FileInstallHandle  `json:"handle"`
	Grant          SignedGrant                  `json:"grant"`
	Result         substrate.FileInstallResult  `json:"result"`
	Phase          string                       `json:"phase"`
	DeadlineBootNS int64                        `json:"deadline_boot_ns"`
	Parent         installIdentity              `json:"parent"`
	StageName      string                       `json:"stage_name"`
	Stage          installIdentity              `json:"stage"`
	Anchor         installIdentity              `json:"anchor"`
	Original       installIdentity              `json:"original"`
	OriginalExists bool                         `json:"original_exists"`
}

func (e *Engine) installStore() Engine { return Engine{Root: filepath.Join(e.Root, ".installs")} }
func (e *Engine) saveInstall(r FileInstallRecord) error {
	return atomicJSON(filepath.Join(e.Root, ".installs", r.Request.ID+".json"), r)
}
func (e *Engine) loadInstall(id string) (FileInstallRecord, error) {
	var r FileInstallRecord
	if !validID.MatchString(id) {
		return r, ErrConflict
	}
	err := readJSON(filepath.Join(e.Root, ".installs", id+".json"), &r)
	if err != nil {
		return r, err
	}
	if r.Phase == "cancelled-before-start" {
		g, h := r.Grant.Grant, r.Handle
		if r.Version != 1 || h.ID != id || h.NodeID != "" || h.Digest == "" || g.Operation != "guest_file_install" || g.ActionID != id || g.Digest != h.Digest || r.Grant.BootID != h.BootID || g.Scope.TopologyUID != h.TopologyUID || g.Scope.ResourceUID != h.ResourceUID || g.Scope.Generation != h.Generation || g.Epoch != h.Epoch || !r.Result.Quiescent || r.Result.Outcome != substrate.FileInstallCancelled || r.Request.ID != "" || controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, h.BootID, r.Grant.IssuedAt) != nil {
			return r, ErrConflict
		}
		return r, nil
	}
	digest, err := r.Request.Digest()
	if err != nil || r.Version != 1 || r.Request.ID != id || r.Handle.ID != id || r.Handle.Digest != digest || r.Handle.NodeID != "" || r.Grant.Grant.Digest != digest || r.Grant.Grant.ActionID != id || r.Handle.BootID != r.Grant.BootID || r.Grant.Grant.Scope.TopologyUID != r.Handle.TopologyUID || r.Grant.Grant.Scope.ResourceUID != r.Handle.ResourceUID || r.Grant.Grant.Scope.Generation != r.Handle.Generation || r.Grant.Grant.Epoch != r.Handle.Epoch || r.DeadlineBootNS <= 0 || r.Parent.Inode == 0 || !strings.HasPrefix(r.StageName, ".sysbox-install-") || filepath.Base(r.StageName) != r.StageName {
		return r, ErrConflict
	}
	if controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, r.Grant.BootID, r.Grant.IssuedAt) != nil || r.Grant.Grant.Operation != "guest_file_install" {
		return r, ErrUnauthorized
	}
	if r.Result.SHA256 != r.Request.SHA256 || r.Result.Size != r.Request.Size || r.Result.Mode != r.Request.Mode {
		return r, ErrConflict
	}
	switch r.Phase {
	case "committed":
		if !r.Result.Quiescent || r.Result.Outcome != substrate.FileInstallCommitted || r.Anchor.Inode == 0 || r.Stage.Inode == 0 {
			return r, ErrConflict
		}
	case "cancelled":
		if !r.Result.Quiescent || r.Result.Outcome != substrate.FileInstallCancelled {
			return r, ErrConflict
		}
	case "preparing", "prepared", "commit_intent":
		if r.Result.Quiescent || r.Result.Outcome != substrate.FileInstallUnknown {
			return r, ErrConflict
		}
	default:
		return r, ErrConflict
	}
	return r, nil
}
func (e *Engine) listInstalls() ([]FileInstallRecord, error) {
	entries, err := os.ReadDir(filepath.Join(e.Root, ".installs"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []FileInstallRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		r, err := e.loadInstall(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func installResult(r FileInstallRecord, outcome string, quiet bool) substrate.FileInstallResult {
	return substrate.FileInstallResult{Outcome: outcome, Quiescent: quiet, SHA256: r.Request.SHA256, Size: r.Request.Size, Mode: r.Request.Mode}
}
func installLive(ctx context.Context, r FileInstallRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	boot, err := BootID()
	if err != nil {
		return err
	}
	now, err := bootNanos()
	if err != nil {
		return err
	}
	if boot != r.Handle.BootID || now >= r.DeadlineBootNS {
		return ErrUnauthorized
	}
	return nil
}
func (e *Engine) checkpointInstall(phase string) error {
	if e.installHook != nil {
		return e.installHook(phase)
	}
	return nil
}

func (e *Engine) installCancelled(h substrate.FileInstallHandle) (bool, error) {
	var proof substrate.FileInstallHandle
	err := readJSON(filepath.Join(e.Root, ".installs", ".cancels", h.ID+".json"), &proof)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if proof != h {
		return false, ErrConflict
	}
	return true, nil
}
func (e *Engine) checkInstallLive(ctx context.Context, r FileInstallRecord) error {
	if err := installLive(ctx, r); err != nil {
		return err
	}
	cancelled, err := e.installCancelled(r.Handle)
	if err != nil {
		return err
	}
	if cancelled {
		return context.Canceled
	}
	return nil
}
func (e *Engine) lockInstallCommit(ctx context.Context, id string) (func(), error) {
	store := Engine{Root: filepath.Join(e.Root, ".installs", ".commits")}
	return store.lock(ctx, id)
}
func (e *Engine) requestInstallCancellation(ctx context.Context, h substrate.FileInstallHandle) error {
	if !validID.MatchString(h.ID) {
		return ErrConflict
	}
	unlock, err := e.lockInstallCommit(ctx, h.ID)
	if err != nil {
		return err
	}
	defer unlock()
	exists, err := e.installCancelled(h)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return atomicJSON(filepath.Join(e.Root, ".installs", ".cancels", h.ID+".json"), h)
}

func (e *Engine) InstallFile(ctx context.Context, req substrate.FileInstallRequest, grant SignedGrant, data io.Reader) (FileInstallRecord, error) {
	var empty FileInstallRecord
	digest, err := req.Digest()
	if err != nil {
		return empty, err
	}
	boot, err := BootID()
	if err != nil {
		return empty, err
	}
	if controlplane.VerifyExecutionGrant(e.PublicKey, grant, boot, time.Now()) != nil || grant.Grant.Operation != "guest_file_install" || grant.Grant.Digest != digest || grant.Grant.ActionID != req.ID || grant.Grant.Scope.ResourceUID == "" || grant.Grant.Scope.Generation < 1 {
		return empty, ErrUnauthorized
	}
	parent, err := e.installParent(req.Path)
	if err != nil {
		return empty, err
	}
	defer parent.Close()
	epochPath, epoch, scopeUnlock, err := e.lockScope(ctx, grant.Grant)
	if err != nil {
		return empty, err
	}
	defer func() { scopeUnlock() }()
	store := e.installStore()
	unlock, err := store.lock(ctx, req.ID)
	if err != nil {
		return empty, err
	}
	defer unlock()
	handle := substrate.FileInstallHandle{ID: req.ID, Digest: digest, BootID: boot, TopologyUID: grant.Grant.Scope.TopologyUID, ResourceUID: grant.Grant.Scope.ResourceUID, Generation: grant.Grant.Scope.Generation, Epoch: grant.Grant.Epoch}
	prior, err := e.loadInstall(req.ID)
	if err == nil {
		if !reflect.DeepEqual(prior.Handle, handle) || prior.Grant.Grant.SessionID != grant.Grant.SessionID {
			return empty, ErrConflict
		}
		if prior.Phase == "cancelled-before-start" {
			return prior, nil
		}
		return e.recoverInstall(ctx, prior)
	}
	if !os.IsNotExist(err) {
		return empty, err
	}
	if err = e.scopeQuiescent(grant.Grant); err != nil {
		return empty, err
	}
	if data == nil {
		return empty, fmt.Errorf("installation body required")
	}
	switch data.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader, io.ReadCloser:
	default:
		return empty, fmt.Errorf("installation stream must be cancellable")
	}
	r := FileInstallRecord{Version: 1, Request: req, Handle: handle, Grant: grant, Phase: "preparing", StageName: ".sysbox-install-" + rand.Text()}
	r.Parent, err = installFDIdentity(parent)
	if err != nil {
		return empty, err
	}
	r.Original, r.OriginalExists, err = installEntry(parent, filepath.Base(req.Path))
	if err != nil {
		return empty, err
	}
	budget, err := controlplane.ExecutionGrantBudget(grant, time.Now())
	if err != nil {
		return empty, err
	}
	now, err := bootNanos()
	if err != nil {
		return empty, err
	}
	r.DeadlineBootNS = now + int64(budget)
	r.Result = installResult(r, substrate.FileInstallUnknown, false)
	epoch.Epoch, epoch.Session = grant.Grant.Epoch, grant.Grant.SessionID
	if err = atomicJSON(epochPath, epoch); err != nil {
		return empty, err
	}
	if err = e.saveInstall(r); err != nil {
		return r, err
	}
	// Release admission while copying. The persisted nonquiescent record blocks
	// every later task/install of this resource, even across another process.
	scopeUnlock()
	scopeUnlock = func() {}
	if err = unix.Mkdirat(int(parent.Fd()), r.StageName, 0700); err != nil {
		return r, err
	}
	stage, err := installChildDirectory(parent, r.StageName)
	if err != nil {
		return r, err
	}
	defer stage.Close()
	r.Stage, err = installFDIdentity(stage)
	if err != nil {
		return r, err
	}
	if err = parent.Sync(); err != nil {
		return r, err
	}
	if err = e.saveInstall(r); err != nil {
		return r, err
	}
	file, err := openInstallFile(stage, "payload", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return r, err
	}
	defer file.Close()
	copyNow, err := bootNanos()
	if err != nil {
		return r, err
	}
	copyCtx, cancel := context.WithTimeout(ctx, time.Duration(r.DeadlineBootNS-copyNow))
	defer cancel()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-copyCtx.Done():
				return
			case <-ticker.C:
				if cancelled, err := e.installCancelled(r.Handle); err != nil || cancelled {
					cancel()
					return
				}
			}
		}
	}()
	if closer, ok := data.(io.ReadCloser); ok {
		stop := context.AfterFunc(copyCtx, func() { _ = closer.Close() })
		defer stop()
	}
	hash := sha256.New()
	reader := &installReader{engine: e, ctx: copyCtx, record: r, reader: data}
	written, copyErr := io.CopyN(io.MultiWriter(file, hash), reader, req.Size)
	if copyErr == nil {
		var extra [1]byte
		n, err := io.ReadFull(reader, extra[:])
		if n != 0 {
			copyErr = fmt.Errorf("installation body exceeds declared size")
		} else if !errors.Is(err, io.EOF) {
			copyErr = fmt.Errorf("installation body did not complete: %w", err)
		}
	}
	if copyErr != nil || written != req.Size || fmt.Sprintf("%x", hash.Sum(nil)) != req.SHA256 {
		if copyErr == nil {
			copyErr = fmt.Errorf("installation checksum or size mismatch")
		}
		r.Phase = "cancelled"
		r.Result = installResult(r, substrate.FileInstallCancelled, true)
		if err = e.saveInstall(r); err != nil {
			return r, err
		}
		return r, copyErr
	}
	if err = e.checkInstallLive(ctx, r); err != nil {
		return r, err
	}
	if err = file.Chmod(os.FileMode(req.Mode)); err != nil {
		return r, err
	}
	if err = file.Sync(); err != nil {
		return r, err
	}
	r.Anchor, err = installFDIdentity(file)
	if err != nil {
		return r, err
	}
	if err = unix.Linkat(int(stage.Fd()), "payload", int(stage.Fd()), "anchor", 0); err != nil {
		return r, err
	}
	if err = stage.Sync(); err != nil {
		return r, err
	}
	r.Phase = "prepared"
	if err = e.saveInstall(r); err != nil {
		return r, err
	}
	if err = e.checkpointInstall("prepared"); err != nil {
		return r, err
	}
	commitUnlock, err := e.lockInstallCommit(ctx, req.ID)
	if err != nil {
		return r, err
	}
	defer commitUnlock()
	if err = e.checkInstallLive(ctx, r); err != nil {
		return r, err
	}
	current, exists, err := installEntry(parent, filepath.Base(req.Path))
	if err != nil {
		return r, err
	}
	if exists != r.OriginalExists || current != r.Original {
		return r, ErrConflict
	}
	r.Phase = "commit_intent"
	if err = e.saveInstall(r); err != nil {
		return r, err
	}
	if err = e.checkpointInstall("commit_intent"); err != nil {
		return r, err
	}
	if err = e.checkInstallLive(ctx, r); err != nil {
		return r, err
	}
	if err = unix.Renameat(int(stage.Fd()), "payload", int(parent.Fd()), filepath.Base(req.Path)); err != nil {
		return r, err
	}
	if err = e.checkpointInstall("renamed"); err != nil {
		return r, err
	}
	if err = parent.Sync(); err != nil {
		return r, err
	}
	if err = stage.Sync(); err != nil {
		return r, err
	}
	r.Phase = "committed"
	r.Result = installResult(r, substrate.FileInstallCommitted, true)
	return r, e.saveInstall(r)
}

type installReader struct {
	engine *Engine
	ctx    context.Context
	record FileInstallRecord
	reader io.Reader
}

func (r *installReader) Read(p []byte) (int, error) {
	if err := r.engine.checkInstallLive(r.ctx, r.record); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (e *Engine) ObserveFileInstall(ctx context.Context, h substrate.FileInstallHandle) (FileInstallRecord, error) {
	return e.observeInstall(ctx, h)
}
func (e *Engine) CancelFileInstall(ctx context.Context, h substrate.FileInstallHandle, grants ...SignedGrant) (FileInstallRecord, error) {
	if len(grants) == 0 {
		r, err := e.loadInstall(h.ID)
		if err != nil {
			return r, err
		}
		if r.Handle != h {
			return r, ErrConflict
		}
		if err = e.requestInstallCancellation(ctx, h); err != nil {
			return r, err
		}
		return e.observeInstall(ctx, h)
	}
	if len(grants) != 1 {
		return FileInstallRecord{}, ErrUnauthorized
	}
	grant := grants[0]
	g := grant.Grant
	boot, err := BootID()
	if err != nil {
		return FileInstallRecord{}, err
	}
	if h.NodeID != "" || grant.BootID != h.BootID || controlplane.VerifyExecutionGrant(e.PublicKey, grant, h.BootID, grant.IssuedAt) != nil || g.Operation != "guest_file_install" || g.ActionID != h.ID || g.Digest != h.Digest || g.Epoch != h.Epoch || g.Scope.TopologyUID != h.TopologyUID || g.Scope.ResourceUID != h.ResourceUID || g.Scope.Generation != h.Generation {
		return FileInstallRecord{}, ErrUnauthorized
	}
	// Cancellation intent has its own short lock, independent of the writer's
	// long copy lock. Once durable it prevents any not-yet-linearized rename.
	if existing, loadErr := e.loadInstall(h.ID); loadErr == nil {
		if existing.Handle != h {
			return existing, ErrConflict
		}
		if err = e.requestInstallCancellation(ctx, h); err != nil {
			return existing, err
		}
	} else if !os.IsNotExist(loadErr) {
		return existing, loadErr
	} else if boot != h.BootID {
		return existing, ErrUnauthorized
	} else {
		if err = e.requestInstallCancellation(ctx, h); err != nil {
			return existing, err
		}
	}
	// Existing historical outcomes do not acquire fresh scope authority.
	// Release the per-install lock before scope admission for a missing record
	// so every writer retains the same scope-before-install lock order.
	store := e.installStore()
	initialUnlock, err := store.lock(ctx, h.ID)
	if err != nil {
		return FileInstallRecord{}, err
	}
	existing, loadErr := e.loadInstall(h.ID)
	if loadErr == nil {
		defer initialUnlock()
		if existing.Handle != h {
			return existing, ErrConflict
		}
		return e.recoverInstall(ctx, existing)
	}
	initialUnlock()
	if !os.IsNotExist(loadErr) {
		return existing, loadErr
	}
	if boot != h.BootID {
		return FileInstallRecord{}, ErrUnauthorized
	}
	epochPath, epoch, releaseScope, err := e.lockScope(ctx, g)
	if err != nil {
		return FileInstallRecord{}, err
	}
	defer releaseScope()
	unlock, err := store.lock(ctx, h.ID)
	if err != nil {
		return FileInstallRecord{}, err
	}
	defer unlock()
	r, err := e.loadInstall(h.ID)
	if err == nil {
		if r.Handle != h {
			return r, ErrConflict
		}
		if r.Phase == "cancelled-before-start" {
			return r, nil
		}
		return e.recoverInstall(ctx, r)
	}
	if !os.IsNotExist(err) {
		return r, err
	}
	if err = e.scopeQuiescent(g); err != nil {
		return r, err
	}
	epoch.Epoch, epoch.Session = g.Epoch, g.SessionID
	if err = atomicJSON(epochPath, epoch); err != nil {
		return r, err
	}
	r = FileInstallRecord{Version: 1, Handle: h, Grant: grant, Phase: "cancelled-before-start", Result: substrate.FileInstallResult{Outcome: substrate.FileInstallCancelled, Quiescent: true}}
	// No request path or target write is inferred from a cancellation proof.
	if err = atomicJSON(filepath.Join(e.Root, ".installs", h.ID+".json"), r); err != nil {
		return r, err
	}
	return r, nil
}
func (e *Engine) observeInstall(ctx context.Context, h substrate.FileInstallHandle) (FileInstallRecord, error) {
	store := e.installStore()
	unlock, err := store.lock(ctx, h.ID)
	if err != nil {
		return FileInstallRecord{}, err
	}
	defer unlock()
	r, err := e.loadInstall(h.ID)
	if err != nil {
		return r, err
	}
	if !reflect.DeepEqual(r.Handle, h) {
		return r, ErrConflict
	}
	if r.Phase == "cancelled-before-start" {
		return r, nil
	}
	return e.recoverInstall(ctx, r)
}

func (e *Engine) recoverInstall(ctx context.Context, r FileInstallRecord) (FileInstallRecord, error) {
	if err := ctx.Err(); err != nil {
		return r, err
	}
	// Durable terminal records are historical effect evidence. Later authorized
	// writes or deletion of that path cannot erase an already committed result.
	if r.Phase == "committed" || r.Phase == "cancelled" || r.Phase == "cancelled-before-start" {
		return r, nil
	}
	unknown := func(err error) (FileInstallRecord, error) {
		r.Result = installResult(r, substrate.FileInstallUnknown, false)
		_ = e.saveInstall(r)
		return r, err
	}
	parent, err := e.installParent(r.Request.Path)
	if err != nil {
		return unknown(err)
	}
	defer parent.Close()
	identity, err := installFDIdentity(parent)
	if err != nil || identity != r.Parent {
		return unknown(ErrConflict)
	}
	if r.Phase == "preparing" {
		r.Phase = "cancelled"
		r.Result = installResult(r, substrate.FileInstallCancelled, true)
		return r, e.saveInstall(r)
	}
	stage, err := installChildDirectory(parent, r.StageName)
	if err != nil {
		return unknown(err)
	}
	defer stage.Close()
	identity, err = installFDIdentity(stage)
	if err != nil || identity != r.Stage {
		return unknown(ErrConflict)
	}
	anchor, exists, err := installEntry(stage, "anchor")
	if err != nil || !exists || anchor != r.Anchor {
		return unknown(ErrConflict)
	}
	target, targetExists, err := installEntry(parent, filepath.Base(r.Request.Path))
	if err != nil {
		return unknown(err)
	}
	if targetExists && target == r.Anchor {
		if err = verifyInstalledFile(parent, filepath.Base(r.Request.Path), r); err != nil {
			return unknown(err)
		}
		if err = parent.Sync(); err != nil {
			return unknown(err)
		}
		if err = stage.Sync(); err != nil {
			return unknown(err)
		}
		r.Phase = "committed"
		r.Result = installResult(r, substrate.FileInstallCommitted, true)
		return r, e.saveInstall(r)
	}
	source, sourceExists, err := installEntry(stage, "payload")
	if err != nil || !sourceExists || source != r.Anchor || targetExists != r.OriginalExists || target != r.Original {
		return unknown(ErrConflict)
	}
	if err = verifyInstalledFile(stage, "payload", r); err != nil {
		return unknown(err)
	}
	r.Phase = "cancelled"
	r.Result = installResult(r, substrate.FileInstallCancelled, true)
	return r, e.saveInstall(r)
}
