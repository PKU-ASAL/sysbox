package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

type sshGuestRecord struct {
	Version  int                                      `json:"version"`
	Ref      TargetRef                                `json:"ref"`
	Request  substrate.SSHAccessMutation              `json:"request"`
	Handle   substrate.SSHAccessHandle                `json:"handle"`
	Grant    controlplane.SignedExecutionGrant        `json:"grant"`
	Result   substrate.SSHAccessResult                `json:"result"`
	Receipt  *controlplane.SignedExecutionStopReceipt `json:"receipt,omitempty"`
	Reported bool                                     `json:"reported"`
}

type SSHGuestDispatcher struct {
	*Dispatcher
	Report func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
}

func sshGuestPath(root string, h substrate.SSHAccessHandle) string {
	return filepath.Join(root, fmt.Sprintf("ssh-%x.json", []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", h.TopologyUID, h.ResourceUID, h.ID, h.Epoch))))
}
func (d *SSHGuestDispatcher) saveSSH(r sshGuestRecord) error {
	return saveRecord(d.Root, sshGuestPath(d.Root, r.Handle), r)
}
func (d *SSHGuestDispatcher) loadSSH(path string) (sshGuestRecord, error) {
	var r sshGuestRecord
	raw, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	if len(raw) > 1<<20 {
		return r, guesttask.ErrConflict
	}
	e = json.Unmarshal(raw, &r)
	return r, e
}

func (d *SSHGuestDispatcher) Mutate(ctx context.Context, ref TargetRef, req substrate.SSHAccessMutation, grant controlplane.SignedExecutionGrant) (substrate.SSHAccessResult, error) {
	var zero substrate.SSHAccessResult
	if d == nil || d.Dispatcher == nil || d.Report == nil || len(d.IssuerKey) != ed25519.PublicKeySize || len(d.HostKey) != ed25519.PrivateKeySize {
		return zero, effectgate.ErrFenced
	}
	digest, e := req.Digest()
	if e != nil {
		return zero, e
	}
	if !controlplane.IsGuestSSHAccessOperation(grant.Grant.Operation) || grant.Grant.Digest != digest || grant.Grant.ActionID != req.ID || grant.Grant.Scope != ref.Scope || grant.Grant.TargetBootID == "" {
		return zero, effectgate.ErrFenced
	}
	if e = controlplane.VerifyExecutionGrant(d.IssuerKey, grant, grant.BootID, time.Now()); e != nil {
		return zero, e
	}
	target, e := d.target(ctx, ref, grant.Grant)
	if e != nil {
		return zero, e
	}
	access, ok := target.Connection.(substrate.ConnectionSSHAccess)
	if !ok {
		return zero, substrate.ErrNotSupported
	}
	resultHandle := substrate.SSHAccessHandle{ID: req.ID, Digest: digest, NodeID: ref.Node, User: req.Config.User, AccessID: req.Config.AccessID, Revision: req.ExpectedRevision + 1, BootID: grant.BootID, TopologyUID: ref.Scope.TopologyUID, ResourceUID: ref.Scope.ResourceUID, Generation: ref.Scope.Generation, Epoch: grant.Grant.Epoch}
	if prior, loadErr := d.loadSSH(sshGuestPath(d.Root, resultHandle)); loadErr == nil {
		priorDigest, _ := prior.Request.Digest()
		if priorDigest != digest || prior.Grant.Grant.Digest != grant.Grant.Digest {
			return substrate.SSHAccessResult{Handle: resultHandle}, guesttask.ErrConflict
		}
		if prior.Receipt != nil {
			return prior.Result, nil
		}
		target, targetErr := d.target(ctx, ref, grant.Grant)
		if targetErr != nil {
			return substrate.SSHAccessResult{Handle: resultHandle}, targetErr
		}
		access, ok := target.Connection.(substrate.ConnectionSSHAccess)
		if !ok {
			return substrate.SSHAccessResult{Handle: resultHandle}, substrate.ErrNotSupported
		}
		observed, observeErr := access.ObserveSSHAccess(ctx, resultHandle)
		if observeErr != nil {
			return substrate.SSHAccessResult{Handle: resultHandle}, observeErr
		}
		prior.Result = observed
		if finishErr := d.finishSSH(ctx, &prior); finishErr != nil {
			return observed, finishErr
		}
		return observed, nil
	}
	record := sshGuestRecord{Version: 1, Ref: ref, Request: req, Handle: resultHandle, Grant: grant, Result: substrate.SSHAccessResult{Handle: resultHandle}}
	if e = d.saveSSH(record); e != nil {
		return substrate.SSHAccessResult{Handle: resultHandle}, e
	}
	result, e := access.MutateSSHAccess(transport.WithTaskGrant(ctx, req.ID, grant), req)
	if e != nil {
		return substrate.SSHAccessResult{Handle: resultHandle}, e
	}
	result.Handle.NodeID = ref.Node
	record.Result = result
	if e = d.finishSSH(ctx, &record); e != nil {
		return result, e
	}
	return result, nil
}
func (d *SSHGuestDispatcher) finishSSH(ctx context.Context, r *sshGuestRecord) error {
	if r.Result.Handle.ID != r.Handle.ID || r.Result.Handle.Digest != r.Handle.Digest || r.Result.Handle.Revision != r.Handle.Revision || r.Result.Present == r.Request.Delete {
		return guesttask.ErrConflict
	}
	receipt, e := controlplane.SignGuestSSHAccessStopReceipt(d.HostKey, r.Grant.Grant, d.HostBootID, r.Request, r.Result, rand.Text(), time.Now())
	if e != nil {
		return e
	}
	r.Receipt = &receipt
	if e = d.saveSSH(*r); e != nil {
		return e
	}
	raw, _ := json.Marshal(receipt)
	if e = d.Report(ctx, r.Request.ID, receipt, raw); e != nil {
		return e
	}
	r.Reported = true
	return d.saveSSH(*r)
}
func (d *SSHGuestDispatcher) Recover(ctx context.Context) error {
	entries, e := os.ReadDir(d.Root)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "ssh-") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		r, e := d.loadSSH(filepath.Join(d.Root, entry.Name()))
		if e != nil {
			return e
		}
		if r.Receipt == nil {
			target, err := d.target(ctx, r.Ref, r.Grant.Grant)
			if err != nil {
				return err
			}
			access, ok := target.Connection.(substrate.ConnectionSSHAccess)
			if !ok {
				return substrate.ErrNotSupported
			}
			observed, err := access.ObserveSSHAccess(ctx, r.Handle)
			if err != nil {
				return err
			}
			observed.Handle.NodeID = r.Ref.Node
			r.Result = observed
		}
		if r.Receipt == nil {
			if err := d.finishSSH(ctx, &r); err != nil {
				return err
			}
			continue
		}
		if r.Receipt != nil && !r.Reported {
			raw, _ := json.Marshal(*r.Receipt)
			if e = d.Report(ctx, r.Request.ID, *r.Receipt, raw); e != nil {
				return e
			}
			r.Reported = true
			if e = d.saveSSH(r); e != nil {
				return e
			}
		}
	}
	return nil
}
