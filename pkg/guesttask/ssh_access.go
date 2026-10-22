package guesttask

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

// The configuration is the authorization source consumed by sshd. Its mutation
// result is in the same atomic record; no second key-file publication can race
// rotation or leave a successful journal referring to different key bytes.
type sshAccessReceipt struct {
	Request substrate.SSHAccessMutation `json:"request"`
	Result  substrate.SSHAccessResult   `json:"result"`
	Grant   SignedGrant                 `json:"grant"`
}

type sshAccessRecord struct {
	Version   int                         `json:"version"`
	CurrentID string                      `json:"current_id"`
	History   map[string]sshAccessReceipt `json:"history"`
}

func (r sshAccessRecord) current() sshAccessReceipt { return r.History[r.CurrentID] }

func (e *Engine) sshAccessStore() Engine {
	return Engine{Root: filepath.Join(e.Root, ".ssh-access")}
}

func sshAccessName(user, accessID string) string {
	return fmt.Sprintf("%x.json", sha256.Sum256([]byte(user+"\x00"+accessID)))
}

func sshAccessOperation(remove bool) string {
	if remove {
		return "guest_ssh_access_delete"
	}
	return "guest_ssh_access_apply"
}

func (e *Engine) validateSSHReceipt(receipt sshAccessReceipt) error {
	h, signed := receipt.Result.Handle, receipt.Grant
	g := signed.Grant
	digest, err := receipt.Request.Digest()
	if err != nil || digest != h.Digest || receipt.Request.ID != h.ID || receipt.Request.Config.User != h.User ||
		receipt.Request.Config.AccessID != h.AccessID || receipt.Request.ExpectedRevision == math.MaxUint64 ||
		receipt.Request.ExpectedRevision+1 != h.Revision || receipt.Request.Delete == receipt.Result.Present ||
		h.NodeID != "" || h.ID == "" || h.Digest == "" || h.Revision == 0 || h.BootID != signed.BootID || h.Epoch != g.Epoch ||
		h.TopologyUID != g.Scope.TopologyUID || h.ResourceUID != g.Scope.ResourceUID || h.Generation != g.Scope.Generation ||
		h.ResourceUID == "" || h.Generation < 1 || g.ActionID != h.ID || g.Digest != h.Digest ||
		g.Operation != sshAccessOperation(!receipt.Result.Present) || substrate.ValidateSSHAccessUser(h.User) != nil || !validID.MatchString(h.AccessID) {
		return ErrConflict
	}
	if controlplane.VerifyExecutionGrant(e.PublicKey, signed, h.BootID, signed.IssuedAt) != nil {
		return ErrUnauthorized
	}
	return nil
}

func (e *Engine) readSSHAccess(parent *os.File, name string) (sshAccessRecord, error) {
	var record sshAccessRecord
	file, err := openInstallFile(parent, name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return record, err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &stat); err != nil {
		return record, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Nlink != 1 {
		return record, ErrConflict
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxInput*2+1))
	if err != nil {
		return record, err
	}
	if len(raw) > MaxInput*2 || json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.History == nil {
		return record, ErrConflict
	}
	current := record.current()
	if record.CurrentID != current.Result.Handle.ID || name != sshAccessName(current.Result.Handle.User, current.Result.Handle.AccessID) {
		return record, ErrConflict
	}
	if uint64(len(record.History)) != current.Result.Handle.Revision {
		return record, ErrConflict
	}
	seen := make(map[uint64]bool, len(record.History))
	for id, receipt := range record.History {
		h := receipt.Result.Handle
		if id != h.ID || h.User != current.Result.Handle.User || h.AccessID != current.Result.Handle.AccessID ||
			receipt.Grant.Grant.Scope != current.Grant.Grant.Scope || h.Revision > current.Result.Handle.Revision ||
			h.Epoch > current.Result.Handle.Epoch || seen[h.Revision] {
			return record, ErrConflict
		}
		if err := e.validateSSHReceipt(receipt); err != nil {
			return record, err
		}
		seen[h.Revision] = true
	}
	return record, nil
}

func (e *Engine) MutateSSHAccess(ctx context.Context, req substrate.SSHAccessMutation, grant SignedGrant) (substrate.SSHAccessResult, error) {
	var empty substrate.SSHAccessResult
	digest, err := req.Digest()
	if err != nil {
		return empty, err
	}
	boot, err := BootID()
	if err != nil {
		return empty, err
	}
	g := grant.Grant
	if controlplane.VerifyExecutionGrant(e.PublicKey, grant, boot, time.Now()) != nil ||
		g.Operation != sshAccessOperation(req.Delete) || g.ActionID != req.ID || g.Digest != digest || g.Scope.ResourceUID == "" || g.Scope.Generation < 1 {
		return empty, ErrUnauthorized
	}
	budget, err := controlplane.ExecutionGrantBudget(grant, time.Now())
	if err != nil {
		return empty, err
	}
	deadline := time.Now().Add(budget)
	epochPath, epoch, unlockScope, err := e.lockScope(ctx, g)
	if err != nil {
		return empty, err
	}
	defer unlockScope()
	if _, err := e.loadSSHStop(req, grant); err == nil {
		return empty, ErrUnauthorized
	} else if !os.IsNotExist(err) {
		return empty, err
	}
	store := e.sshAccessStore()
	unlock, err := store.lock(ctx, "access")
	if err != nil {
		return empty, err
	}
	defer unlock()
	parent, err := openInstallRoot(store.Root)
	if err != nil {
		return empty, err
	}
	defer parent.Close()
	name := sshAccessName(req.Config.User, req.Config.AccessID)
	prior, err := e.readSSHAccess(parent, name)
	if err != nil && !os.IsNotExist(err) {
		return empty, err
	}
	if err == nil {
		if prior.current().Grant.Grant.Scope != g.Scope {
			return empty, ErrConflict
		}
		if receipt, exists := prior.History[req.ID]; exists {
			old := receipt.Grant.Grant
			if receipt.Result.Handle.Digest != digest || receipt.Result.Handle.BootID != boot ||
				old.Epoch != g.Epoch || old.SessionID != g.SessionID || old.AgentID != g.AgentID || old.CommandID != g.CommandID || old.Attempt != g.Attempt || old.ParentEpoch != g.ParentEpoch {
				return empty, ErrConflict
			}
			if err := e.validateSSHReceipt(receipt); err != nil {
				return empty, err
			}
			return receipt.Result, nil
		}
		if req.ExpectedRevision != prior.current().Result.Handle.Revision {
			return empty, ErrConflict
		}
	} else {
		if req.ExpectedRevision != 0 || req.Delete {
			return empty, ErrConflict
		}
		prior.History = make(map[string]sshAccessReceipt)
	}
	if req.ExpectedRevision == math.MaxUint64 {
		return empty, ErrConflict
	}
	if err = e.scopeQuiescent(g); err != nil {
		return empty, err
	}
	result := substrate.SSHAccessResult{Present: !req.Delete, Handle: substrate.SSHAccessHandle{
		ID: req.ID, Digest: digest, User: req.Config.User, AccessID: req.Config.AccessID,
		Revision: req.ExpectedRevision + 1, BootID: boot, TopologyUID: g.Scope.TopologyUID,
		ResourceUID: g.Scope.ResourceUID, Generation: g.Scope.Generation, Epoch: g.Epoch,
	}}
	receipt := sshAccessReceipt{Request: req, Result: result, Grant: grant}
	prior.History[req.ID] = receipt
	record := sshAccessRecord{Version: 1, CurrentID: req.ID, History: prior.History}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > MaxInput*2 {
		return empty, fmt.Errorf("SSH access record exceeds storage limit")
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if !time.Now().Before(deadline) {
		return empty, ErrUnauthorized
	}
	// Fence lower epochs before publishing keys. If the key record cannot be
	// committed, lockScope still permits this same epoch/session to retry.
	epoch.Epoch, epoch.Session = g.Epoch, g.SessionID
	if err = atomicJSON(epochPath, epoch); err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if !time.Now().Before(deadline) {
		return empty, ErrUnauthorized
	}
	if err = atomicJSON(filepath.Join(store.Root, name), record); err != nil {
		return empty, err
	}
	return result, nil
}

func (e *Engine) sshAccessForHandle(h substrate.SSHAccessHandle) (sshAccessRecord, sshAccessReceipt, error) {
	store := e.sshAccessStore()
	parent, err := openInstallRoot(store.Root)
	if err != nil {
		return sshAccessRecord{}, sshAccessReceipt{}, err
	}
	defer parent.Close()
	r, err := e.readSSHAccess(parent, sshAccessName(h.User, h.AccessID))
	if err != nil {
		return r, sshAccessReceipt{}, err
	}
	receipt, found := r.History[h.ID]
	if !found || receipt.Result.Handle != h || receipt.Grant.Grant.Scope != r.current().Grant.Grant.Scope {
		return r, receipt, ErrConflict
	}
	return r, receipt, e.validateSSHReceipt(receipt)
}

func (e *Engine) ObserveSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessResult, error) {
	if err := ctx.Err(); err != nil {
		return substrate.SSHAccessResult{}, err
	}
	_, receipt, err := e.sshAccessForHandle(h)
	return receipt.Result, err
}

func (e *Engine) InspectSSHAccess(ctx context.Context, h substrate.SSHAccessHandle) (substrate.SSHAccessInspection, error) {
	if err := ctx.Err(); err != nil {
		return substrate.SSHAccessInspection{}, err
	}
	r, _, err := e.sshAccessForHandle(h)
	if err != nil {
		return substrate.SSHAccessInspection{Status: "unknown"}, err
	}
	status := "drifted"
	if !r.current().Result.Present {
		status = "absent"
	} else if r.current().Result.Handle == h {
		status = "present"
	}
	return substrate.SSHAccessInspection{Status: status, Revision: r.current().Result.Handle.Revision}, nil
}

// SSHAuthorizedKeys is read-only, including on an uninitialized guest. Corrupt
// records fail the entire lookup before any keys are returned to sshd.
func (e *Engine) SSHAuthorizedKeys(ctx context.Context, user, localIP string, port int) ([]byte, error) {
	if substrate.ValidateSSHAccessUser(user) != nil || (localIP != "" && net.ParseIP(localIP) == nil) || port < 0 || port > 65535 {
		return nil, ErrConflict
	}
	store := e.sshAccessStore()
	parent, err := openInstallRoot(store.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, err := e.readSSHAccess(parent, entry.Name())
		if err != nil {
			return nil, err
		}
		current := r.current()
		cfg := current.Request.Config
		if !current.Result.Present || cfg.User != user || cfg.Port != 0 && cfg.Port != port || cfg.BindIP != "" && localIP != "" && !net.ParseIP(cfg.BindIP).Equal(net.ParseIP(localIP)) {
			continue
		}
		if cfg.BindIP != "" && localIP == "" {
			// AuthorizedKeysCommand cannot receive the local listener address
			// from OpenSSH. Never authorize a bind-specific access on an
			// endpoint we cannot verify.
			continue
		}
		for _, key := range cfg.AuthorizedKeys {
			keys[key] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return nil, nil
	}
	return []byte(strings.Join(ordered, "\n") + "\n"), nil
}
