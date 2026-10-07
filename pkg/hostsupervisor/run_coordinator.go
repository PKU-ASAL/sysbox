package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

// RunCoordinator shares the durable effect boundary of other host actions.
// It is not yet wired to remote Run admission. Callbacks must be constructed
// by trusted host code from the pinned revision and atomic action repository.
type RunCoordinator struct {
	Root       string
	IssuerKey  ed25519.PublicKey
	HostBootID string
	AgentID    string
	Authorize  func(context.Context, controlplane.SignedExecutionGrant) error
}

func (c *RunCoordinator) gate(g controlplane.SignedExecutionGrant) (*effectgate.Gate, error) {
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

func (c *RunCoordinator) Start(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	if err := ValidateRunGrant(g, req, c.HostBootID); err != nil {
		return nil, err
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	return gate.Execute(ctx, g, req.ActionID, effect)
}

// Observe requires the exact signed historical proof. Unknown intent remains
// uncertain after restart and cannot be turned into a successful response.
func (c *RunCoordinator) Observe(ctx context.Context, req RunRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	if err := ValidateRunGrant(g, req, g.BootID); err != nil {
		return nil, err
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, g.BootID, g.IssuedAt); err != nil {
		return nil, err
	}
	gate, err := c.gate(g)
	if err != nil {
		return nil, err
	}
	return gate.ReadOutcome(ctx, g.Grant, req.ActionID)
}
