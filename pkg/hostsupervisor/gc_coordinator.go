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
	if c.Kind != "container" && c.Kind != "network" && c.Kind != "domain" && c.Kind != "firecracker" {
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
	HostKey             ed25519.PrivateKey
	ReportStop          func(context.Context, controlplane.SignedExecutionStopReceipt) error
	Authorize           func(context.Context, controlplane.SignedExecutionGrant) error
	// ValidateInventory re-reads the canonical host inventory under the same
	// coordinator boundary. A dry-run digest is never sufficient authorization.
	ValidateInventory func(context.Context, GCCandidate) error
}

// Recover replays only durable completed GC outcomes. It never invokes a
// provider callback; pending intents remain uncertain until an explicit
// operator decision.
func (c *GCCoordinator) Recover(ctx context.Context) error {
	if c == nil || c.HostBootID == "" || len(c.IssuerKey) != ed25519.PublicKeySize || len(c.HostKey) != ed25519.PrivateKeySize || c.ReportStop == nil {
		return effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.RecoverStops(ctx, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
		return controlplane.SignExecutionStopReceipt(c.HostKey, grant, c.HostBootID, digestHex([]byte(fmt.Sprintf("%s/%d", grant.ActionID, time.Now().UnixNano()))), time.Now().UTC())
	}, c.ReportStop)
}

func (c *GCCoordinator) Delete(ctx context.Context, candidate GCCandidate, g controlplane.SignedExecutionGrant, deleteEffect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	digest, err := candidate.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "gc_delete" || g.Grant.AgentID != c.AgentID || g.Grant.Digest != digest || g.Grant.Scope.TopologyUID != candidate.TopologyUID || g.Grant.Scope.ResourceUID != candidate.ResourceUID || g.Grant.Scope.Generation != candidate.Generation || g.Grant.TargetBootID != c.HostBootID {
		return nil, effectgate.ErrFenced
	}
	if err = controlplane.VerifyExecutionGrant(c.IssuerKey, g, c.HostBootID, time.Now()); err != nil {
		return nil, err
	}
	if c.ValidateInventory == nil {
		return nil, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}
	raw, err := gate.Execute(ctx, g, candidate.ExternalID, func(effectCtx context.Context) (json.RawMessage, error) {
		if err := c.ValidateInventory(effectCtx, candidate); err != nil {
			return nil, err
		}
		return deleteEffect(effectCtx)
	})
	if err != nil {
		return nil, err
	}
	if len(c.HostKey) == ed25519.PrivateKeySize {
		receipt, sealErr := gate.SealStop(ctx, g.Grant, candidate.ExternalID, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
			return controlplane.SignExecutionStopReceipt(c.HostKey, grant, c.HostBootID, digestHex([]byte(fmt.Sprintf("%s/%d", candidate.ExternalID, time.Now().UnixNano()))), time.Now().UTC())
		})
		if sealErr != nil {
			return nil, sealErr
		}
		if c.ReportStop != nil {
			if reportErr := gate.ReportStop(ctx, g.Grant, candidate.ExternalID, func(reportCtx context.Context, _ controlplane.SignedExecutionStopReceipt) error {
				return c.ReportStop(reportCtx, receipt)
			}); reportErr != nil {
				return nil, reportErr
			}
		}
	}
	return raw, nil
}
func (c *GCCoordinator) Observe(ctx context.Context, candidate GCCandidate, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	digest, err := candidate.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "gc_delete" || g.Grant.AgentID != c.AgentID || g.Grant.Scope.TopologyUID != candidate.TopologyUID || g.Grant.Scope.ResourceUID != candidate.ResourceUID || g.Grant.Scope.Generation != candidate.Generation || g.Grant.TargetBootID == "" || g.Grant.Digest != digest {
		return nil, effectgate.ErrFenced
	}
	if err := controlplane.VerifyExecutionGrant(c.IssuerKey, g, g.BootID, g.IssuedAt); err != nil {
		return nil, err
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.ReadOutcome(ctx, g.Grant, candidate.ExternalID)
}
