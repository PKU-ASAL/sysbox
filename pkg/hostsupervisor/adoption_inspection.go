package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

func (c *AdoptionCoordinator) Inspect(ctx context.Context, req controlplane.AdoptionInspectionRequest, g controlplane.SignedExecutionGrant, inspect func(context.Context) (substrate.AdoptionEvidence, error)) (json.RawMessage, error) {
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	grant := g.Grant
	if inspect == nil || c.Report == nil || c.Authorize == nil || len(c.HostKey) != ed25519.PrivateKeySize || grant.Operation != "adoption_inspect" || grant.Digest != digest || grant.AgentID != c.AgentID || req.AgentID != c.AgentID || grant.ActionID != req.ID || grant.Scope != (controlplane.ExecutionScope{TopologyUID: req.TopologyUID, ResourceUID: req.ResourceUID, Generation: req.Generation}) || grant.TargetBootID != c.HostBootID {
		return nil, effectgate.ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	if err := EnsureOwnedDirectory(c.Root, 0700); err != nil {
		return nil, err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error { return c.Authorize(ctx, g) }}
	raw, err := gate.Execute(ctx, g, req.ExternalID, func(ctx context.Context) (json.RawMessage, error) {
		evidence, err := inspect(ctx)
		if err != nil {
			return json.Marshal(controlplane.AdoptionInspectionResult{Error: err.Error()})
		}
		if evidence.Provider != req.Provider || evidence.ExternalID == "" || evidence.Fingerprint == "" || !json.Valid(evidence.State) {
			return nil, effectgate.ErrUncertain
		}
		return json.Marshal(controlplane.AdoptionInspectionResult{Evidence: &evidence})
	})
	if err != nil {
		return nil, err
	}
	return raw, c.finish(ctx, gate, AdoptionRequest{ID: req.ID, ExternalID: req.ExternalID}, grant, raw)
}
