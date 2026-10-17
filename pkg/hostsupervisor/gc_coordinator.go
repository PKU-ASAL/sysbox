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

type GCCandidate = controlplane.GCCandidate

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
	ValidateInventory   func(context.Context, GCCandidate) error
	ValidateReservation func(context.Context, GCCandidate, controlplane.SignedExecutionGrant) error
}

// Recover replays only durable completed GC outcomes. It never invokes a
// provider callback; pending intents remain uncertain until an explicit
// operator decision.
func (c *GCCoordinator) Recover(ctx context.Context) error {
	if c == nil || c.HostBootID == "" || len(c.IssuerKey) != ed25519.PublicKeySize || len(c.HostKey) != ed25519.PrivateKeySize || c.ReportStop == nil {
		return effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.RecoverCompleted(ctx, func(ctx context.Context, grant controlplane.ExecutionGrant, id string) error {
		raw, err := gate.ReadOutcome(ctx, grant, id)
		if err != nil {
			return err
		}
		var outcome gcOutcome
		if err := json.Unmarshal(raw, &outcome); err != nil {
			return err
		}
		digest, err := outcome.Candidate.Digest()
		if err != nil || digest != grant.Digest || outcome.Candidate.ExternalID != id || !gcDeleted(outcome.Result) {
			return effectgate.ErrUncertain
		}
		if _, err = gate.SealStop(ctx, grant, id, func(g controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
			return controlplane.SignGCDeletionReceipt(c.HostKey, g, g.TargetBootID, digestHex([]byte(fmt.Sprintf("%s/%d", id, time.Now().UnixNano()))), outcome.Candidate, time.Now().UTC())
		}); err != nil {
			return err
		}
		return gate.ReportStop(ctx, grant, id, c.ReportStop)
	})
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
	if c.ValidateInventory == nil && c.ValidateReservation == nil {
		return nil, fmt.Errorf("%w: complete GC reference validator unavailable", effectgate.ErrFenced)
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID, Authorize: func(ctx context.Context, _ controlplane.ExecutionGrant) error {
		if c.Authorize == nil {
			return effectgate.ErrFenced
		}
		return c.Authorize(ctx, g)
	}}
	raw, err := gate.Execute(ctx, g, candidate.ExternalID, func(effectCtx context.Context) (json.RawMessage, error) {
		if c.ValidateReservation != nil {
			if err := c.ValidateReservation(effectCtx, candidate, g); err != nil {
				return nil, err
			}
		} else if err := c.ValidateInventory(effectCtx, candidate); err != nil {
			return nil, err
		}
		result, err := deleteEffect(effectCtx)
		if err != nil {
			return nil, err
		}
		if !gcDeleted(result) {
			return nil, effectgate.ErrUncertain
		}
		return json.Marshal(gcOutcome{Candidate: candidate, Result: result})
	})
	if err != nil {
		return nil, err
	}
	if len(c.HostKey) == ed25519.PrivateKeySize {
		receipt, sealErr := gate.SealStop(ctx, g.Grant, candidate.ExternalID, func(grant controlplane.ExecutionGrant) (controlplane.SignedExecutionStopReceipt, error) {
			return controlplane.SignGCDeletionReceipt(c.HostKey, grant, c.HostBootID, digestHex([]byte(fmt.Sprintf("%s/%d", candidate.ExternalID, time.Now().UnixNano()))), candidate, time.Now().UTC())
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
	return decodeGCOutcome(raw, candidate)
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
	raw, err := gate.ReadOutcome(ctx, g.Grant, candidate.ExternalID)
	if err != nil {
		return nil, err
	}
	return decodeGCOutcome(raw, candidate)
}

type gcOutcome struct {
	Candidate GCCandidate     `json:"candidate"`
	Result    json.RawMessage `json:"result"`
}

func gcDeleted(raw json.RawMessage) bool {
	var result struct {
		Deleted bool `json:"deleted"`
	}
	return json.Unmarshal(raw, &result) == nil && result.Deleted
}
func decodeGCOutcome(raw json.RawMessage, candidate GCCandidate) (json.RawMessage, error) {
	var outcome gcOutcome
	if json.Unmarshal(raw, &outcome) != nil || outcome.Candidate != candidate || !gcDeleted(outcome.Result) {
		return nil, effectgate.ErrUncertain
	}
	return outcome.Result, nil
}
