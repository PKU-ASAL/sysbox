package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"time"
)

type SSHAccessCoordinator struct {
	Root                string
	IssuerKey           ed25519.PublicKey
	HostBootID, AgentID string
	Authorize           func(context.Context, controlplane.SignedExecutionGrant) error
	HostKey             ed25519.PrivateKey
	Report              func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
}

func (c *SSHAccessCoordinator) gate(g controlplane.SignedExecutionGrant) (*effectgate.Gate, error) {
	if c.AgentID == "" || g.Grant.AgentID != c.AgentID || c.HostBootID == "" || len(c.IssuerKey) != ed25519.PublicKeySize {
		return nil, effectgate.ErrFenced
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, err
	}
	return &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}, nil
}
func (c *SSHAccessCoordinator) Apply(ctx context.Context, req controlplane.SSHAccessRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return nil, effectgate.ErrFenced
	}
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "ssh_access_apply" || g.Grant.Digest != digest || g.Grant.Scope.TopologyUID != req.Topology || g.Grant.TargetBootID != c.HostBootID {
		return nil, effectgate.ErrFenced
	}
	if err = controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	raw, err := gate.Execute(ctx, g, req.ID, effect)
	if err != nil {
		return nil, err
	}
	if err := c.finish(ctx, gate, req.ID, g.Grant, raw); err != nil {
		return nil, err
	}
	return raw, nil
}
func (c *SSHAccessCoordinator) Delete(ctx context.Context, req controlplane.SSHAccessRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return nil, effectgate.ErrFenced
	}
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "ssh_access_delete" || g.Grant.Digest != digest {
		return nil, effectgate.ErrFenced
	}
	if err = controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	raw, err := gate.Execute(ctx, g, req.ID, effect)
	if err != nil {
		return nil, err
	}
	if err := c.finish(ctx, gate, req.ID, g.Grant, raw); err != nil {
		return nil, err
	}
	return raw, nil
}
func (c *SSHAccessCoordinator) Observe(ctx context.Context, req controlplane.SSHAccessRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Digest != digest {
		return nil, fmt.Errorf("ssh access proof mismatch")
	}
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	raw, err := gate.ReadOutcome(ctx, g.Grant, req.ID)
	if err != nil {
		return nil, err
	}
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return nil, effectgate.ErrFenced
	}
	if err := c.finish(ctx, gate, req.ID, g.Grant, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *SSHAccessCoordinator) finish(ctx context.Context, gate *effectgate.Gate, effectID string, g controlplane.ExecutionGrant, raw json.RawMessage) error {
	if _, err := gate.SealStop(ctx, g, effectID, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignExecutionStopReceipt(c.HostKey, grant, c.HostBootID, rand.Text(), time.Now().UTC())
	}); err != nil {
		return err
	}
	return gate.ReportStop(ctx, g, effectID, func(reportCtx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
		return c.Report(reportCtx, effectID, receipt, raw)
	})
}

func (c *SSHAccessCoordinator) Recover(ctx context.Context) error {
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil || c.HostBootID == "" {
		return effectgate.ErrFenced
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.RecoverStops(ctx, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		if grant.Operation != "ssh_access_apply" && grant.Operation != "ssh_access_delete" {
			return controlplane.SignedExecutionStopReceipt{}, effectgate.ErrFenced
		}
		return controlplane.SignExecutionStopReceipt(c.HostKey, grant, c.HostBootID, rand.Text(), time.Now().UTC())
	}, func(reportCtx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
		return c.Report(reportCtx, receipt.Grant.ActionID, receipt, nil)
	})
}
