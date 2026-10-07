package hostsupervisor

import (
	"context"
	"crypto/ed25519"
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
	return gate.Execute(ctx, g, req.ID, effect)
}
func (c *SSHAccessCoordinator) Delete(ctx context.Context, req controlplane.SSHAccessRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
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
	return gate.Execute(ctx, g, req.ID, effect)
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
	return gate.ReadOutcome(ctx, g.Grant, req.ID)
}
