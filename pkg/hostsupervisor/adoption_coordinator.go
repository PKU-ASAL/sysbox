package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

type AdoptionRequest = controlplane.AdoptionRequest

type AdoptionCoordinator struct {
	AuthorizeFence        func(context.Context, string) (controlplane.AdoptionDispositionProof, error)
	InspectFencedAdoption func(context.Context, controlplane.AdoptionRequest) (substrate.NodeObservation, json.RawMessage, error)
	Root                  string
	IssuerKey             ed25519.PublicKey
	HostBootID, AgentID   string
	Authorize             func(context.Context, controlplane.SignedExecutionGrant) error
	HostKey               ed25519.PrivateKey
	Report                func(context.Context, string, controlplane.SignedExecutionStopReceipt, json.RawMessage) error
}

func (c *AdoptionCoordinator) validate(req AdoptionRequest, g controlplane.SignedExecutionGrant, now time.Time) error {
	digest, err := req.Digest()
	if err != nil {
		return err
	}
	x := g.Grant
	if c.AgentID == "" || req.AgentID != c.AgentID || x.AgentID != c.AgentID || x.ActionID != req.ID || x.Operation != "adoption_apply" || x.Digest != digest || x.Scope.TopologyUID != req.TopologyUID || x.Scope.ResourceUID != req.ResourceUID || x.Scope.Generation != req.Generation || x.TargetBootID != g.BootID {
		return effectgate.ErrFenced
	}
	return controlplane.VerifyExecutionGrant(c.IssuerKey, g, g.BootID, now)
}

func (c *AdoptionCoordinator) finish(ctx context.Context, gate *effectgate.Gate, req AdoptionRequest, g controlplane.ExecutionGrant, raw json.RawMessage) error {
	_, err := gate.SealStop(ctx, g, req.ExternalID, func(owned controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignAdoptionStopReceipt(c.HostKey, owned, c.HostBootID, rand.Text(), raw, time.Now())
	})
	if err != nil {
		return err
	}
	return gate.ReportStop(ctx, g, req.ExternalID, func(ctx context.Context, r controlplane.SignedExecutionStopReceipt) error {
		if err := controlplane.VerifyAdoptionStopReceipt(c.HostKey.Public().(ed25519.PublicKey), r, raw); err != nil {
			return err
		}
		return c.Report(ctx, req.ID, r, raw)
	})
}

func (c *AdoptionCoordinator) Apply(ctx context.Context, req AdoptionRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return nil, effectgate.ErrFenced
	}
	if err := c.validate(req, g, time.Now()); err != nil {
		return nil, err
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, err
	}
	if g.BootID != c.HostBootID {
		return nil, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}
	raw, err := gate.ExecuteWithReturnProof(ctx, g, req.ExternalID, effect)
	if err != nil {
		return nil, err
	}
	return raw, c.finish(ctx, gate, req, g.Grant, raw)
}
func (c *AdoptionCoordinator) Observe(ctx context.Context, req AdoptionRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	if len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return nil, effectgate.ErrFenced
	}
	if err := c.validate(req, g, g.IssuedAt); err != nil {
		return nil, err
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	raw, err := gate.ReadOutcome(ctx, g.Grant, req.ExternalID)
	if errors.Is(err, effectgate.ErrUncertain) {
		raw, err = gate.RecoverReturnedOutcome(ctx, g.Grant, req.ExternalID)
	}
	if err != nil {
		return nil, err
	}
	return raw, c.finish(ctx, gate, req, g.Grant, raw)
}

func (c *AdoptionCoordinator) Recover(ctx context.Context) error {
	if c.AgentID == "" || c.HostBootID == "" || len(c.HostKey) != ed25519.PrivateKeySize || c.Report == nil {
		return effectgate.ErrFenced
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	if err := gate.RecoverReturned(ctx); err != nil {
		return err
	}
	return gate.RecoverCompleted(ctx, func(ctx context.Context, g controlplane.ExecutionGrant, effectID string) error {
		if g.AgentID != c.AgentID || (g.Operation != "adoption_apply" && g.Operation != "adoption_inspect") {
			return effectgate.ErrFenced
		}
		raw, err := gate.ReadOutcome(ctx, g, effectID)
		if err != nil {
			return err
		}
		return c.finish(ctx, gate, AdoptionRequest{ID: g.ActionID, ExternalID: effectID}, g, raw)
	})
}
