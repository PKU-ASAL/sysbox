package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
)

var ErrRunUnknown = errors.New("host run outcome unknown")

type RunRecord struct {
	Version  int                               `json:"version"`
	Request  RunRequest                        `json:"request"`
	Grant    controlplane.SignedExecutionGrant `json:"grant"`
	Receipt  *RunReceipt                       `json:"receipt,omitempty"`
	Phase    string                            `json:"phase"`
	Evidence json.RawMessage                   `json:"evidence,omitempty"`
}

// RunCoordinator owns only durable run intent and completion evidence. The
// effect callback is constructed by the host process from canonical state and
// provider credentials; no worker supplied callback can cross this boundary.
type RunCoordinator struct {
	Root       string
	IssuerKey  ed25519.PublicKey
	HostBootID string
}

func (c *RunCoordinator) path(req RunRequest) string {
	return filepath.Join(c.Root, req.RunID+"-"+req.ActionID+".json")
}
func (c *RunCoordinator) lock(ctx context.Context, req RunRequest) (func(), error) {
	if c.Root == "" || c.HostBootID == "" || len(c.IssuerKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("host run coordinator not configured")
	}
	if err := os.MkdirAll(c.Root, 0700); err != nil {
		return nil, err
	}
	l := flock.New(c.path(req) + ".lock")
	ok, err := l.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !ok {
		l.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return func() { _ = l.Close() }, nil
}
func (c *RunCoordinator) load(req RunRequest) (RunRecord, error) {
	var r RunRecord
	raw, err := os.ReadFile(c.path(req))
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || r.Version != 1 || r.Request != req || r.Grant.Grant.Digest == "" || controlplane.VerifyExecutionGrant(c.IssuerKey, r.Grant, c.HostBootID, r.Grant.IssuedAt) != nil {
		return r, ErrRunUnknown
	}
	return r, nil
}
func (c *RunCoordinator) save(r RunRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Root, ".run-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	if err = os.Rename(name, c.path(r.Request)); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func (c *RunCoordinator) Start(ctx context.Context, req RunRequest, grant controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (RunRecord, error) {
	var zero RunRecord
	if err := ValidateRunGrant(grant, req, c.HostBootID); err != nil {
		return zero, err
	}
	if effect == nil {
		return zero, ErrRunUnknown
	}
	unlock, err := c.lock(ctx, req)
	if err != nil {
		return zero, err
	}
	defer unlock()
	r, err := c.load(req)
	if err == nil {
		return r, nil
	}
	if !os.IsNotExist(err) {
		return zero, err
	}
	r = RunRecord{Version: 1, Request: req, Grant: grant, Phase: "intent"}
	if err = c.save(r); err != nil {
		return zero, err
	}
	result, err := effect(ctx)
	if err != nil {
		return r, fmt.Errorf("%w: %v", ErrRunUnknown, err)
	}
	if !json.Valid(result) {
		return r, ErrRunUnknown
	}
	r.Phase = "complete"
	r.Evidence = append(json.RawMessage(nil), result...)
	if err = c.save(r); err != nil {
		return r, ErrRunUnknown
	}
	return r, nil
}
func (c *RunCoordinator) Observe(ctx context.Context, req RunRequest) (RunRecord, error) {
	unlock, err := c.lock(ctx, req)
	if err != nil {
		return RunRecord{}, err
	}
	defer unlock()
	r, err := c.load(req)
	if err != nil {
		return r, err
	}
	return r, nil
}
