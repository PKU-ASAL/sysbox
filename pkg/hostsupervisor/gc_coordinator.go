package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"time"
)

type GCCandidate struct {
	Kind            string `json:"kind"`
	ExternalID      string `json:"external_id"`
	TopologyUID     string `json:"topology_uid"`
	OwnerUID        string `json:"owner_uid"`
	ResourceUID     string `json:"resource_uid"`
	Generation      int64  `json:"generation"`
	InventoryDigest string `json:"inventory_digest"`
}

func (c GCCandidate) Validate() error {
	if c.Kind != "container" && c.Kind != "network" && c.Kind != "domain" {
		return fmt.Errorf("invalid GC candidate kind")
	}
	if c.ExternalID == "" || c.TopologyUID == "" || c.OwnerUID == "" || c.ResourceUID == "" || c.Generation < 1 || c.InventoryDigest == "" {
		return fmt.Errorf("incomplete GC candidate identity")
	}
	if c.TopologyUID != c.OwnerUID {
		return fmt.Errorf("GC owner and topology identity differ")
	}
	return nil
}
func (c GCCandidate) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(c)
	return digestHex(raw), nil
}
func digestHex(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

type GCCoordinator struct {
	Root                string
	IssuerKey           ed25519.PublicKey
	HostBootID, AgentID string
	Authorize           func(context.Context, controlplane.SignedExecutionGrant) error
}

func (c *GCCoordinator) Delete(ctx context.Context, candidate GCCandidate, g controlplane.SignedExecutionGrant, deleteEffect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	digest, err := candidate.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "gc_delete" || g.Grant.Digest != digest || g.Grant.Scope.TopologyUID != candidate.TopologyUID || g.Grant.Scope.ResourceUID != candidate.ResourceUID || g.Grant.Scope.Generation != candidate.Generation || g.Grant.TargetBootID != c.HostBootID {
		return nil, effectgate.ErrFenced
	}
	if err = controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}
	return gate.Execute(ctx, g, candidate.ExternalID, deleteEffect)
}
func (c *GCCoordinator) Observe(ctx context.Context, candidate GCCandidate, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	digest, err := candidate.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Digest != digest {
		return nil, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.ReadOutcome(ctx, g.Grant, candidate.ExternalID)
}
