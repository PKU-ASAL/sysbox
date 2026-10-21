package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

func (c AuthorityClient) AuthorizeGCFence(ctx context.Context, id string) (controlplane.GCDispositionProof, error) {
	var proof controlplane.GCDispositionProof
	if !recoveryCommandID.MatchString(id) {
		return proof, fmt.Errorf("invalid fence ID")
	}
	err := c.postJSON(ctx, "/v1/agents/"+url.PathEscape(c.Identity.ID)+"/gc-fences/"+url.PathEscape(id)+"/authorize", struct{}{}, &proof)
	return proof, err
}

func (c *GCCoordinator) Disposition(ctx context.Context, id string) (controlplane.GCDispositionProof, error) {
	var zero controlplane.GCDispositionProof
	if c.AuthorizeFence == nil || c.InspectFencedCandidate == nil || len(c.HostKey) != ed25519.PrivateKeySize {
		return zero, effectgate.ErrFenced
	}
	proof, err := c.AuthorizeFence(ctx, id)
	if err != nil {
		return zero, err
	}
	if proof.Request.ID != id || proof.Request.Grant.AgentID != c.AgentID || proof.Fence.NewBootID != c.HostBootID || controlplane.VerifyHostFenceReceipt(proof.Request, proof.Fence) != nil {
		return zero, effectgate.ErrFenced
	}
	digest, err := proof.Candidate.Digest()
	if err != nil || proof.Request.Grant.Operation != "gc_delete" || proof.Request.Grant.Digest != digest {
		return zero, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	pub := c.HostKey.Public().(ed25519.PublicKey)
	prior, priorErr := gate.ReadGCDisposition(ctx, proof.Request)
	if priorErr == nil {
		if controlplane.VerifyGCDisposition(pub, prior) != nil {
			return zero, effectgate.ErrUncertain
		}
		return prior, nil
	}
	if !errors.Is(priorErr, effectgate.ErrNotFound) && !errors.Is(priorErr, effectgate.ErrUncertain) {
		return zero, priorErr
	}
	if len(proof.Signature) == 0 {
		presence, err := c.InspectFencedCandidate(ctx, proof.Candidate)
		if err != nil {
			return zero, err
		}
		proof.Presence = presence
		proof.HostBootID = c.HostBootID
		proof.ObservedAt = time.Now().UTC()
		proof, err = controlplane.SignGCDisposition(c.HostKey, proof)
		if err != nil {
			return zero, err
		}
	} else if err = controlplane.VerifyGCDisposition(pub, proof); err != nil {
		return zero, err
	}
	// Recheck that the freeze still names this exact admitted fence before
	// changing the local journal. Resource inspection itself is read-only.
	if err = gate.FenceGC(ctx, proof, pub, func(ctx context.Context) error {
		current, err := c.AuthorizeFence(ctx, id)
		if err != nil {
			return err
		}
		oldDigest, _ := proof.Request.Digest()
		newDigest, _ := current.Request.Digest()
		if oldDigest != newDigest {
			return effectgate.ErrFenced
		}
		return nil
	}); err != nil {
		return zero, err
	}
	return proof, nil
}
func (c *TaskClient) InspectGCDisposition(ctx context.Context, id string) (controlplane.GCDispositionProof, error) {
	response, err := c.call(ctx, Request{Operation: "gc-disposition", GCFenceID: id})
	if err != nil {
		return controlplane.GCDispositionProof{}, err
	}
	if response.GCDisposition == nil {
		return controlplane.GCDispositionProof{}, fmt.Errorf("missing GC disposition proof")
	}
	return *response.GCDisposition, nil
}

func (c AuthorityClient) GCFencedCleanupProof(ctx context.Context, candidate GCCandidate, g controlplane.SignedExecutionGrant, originalBoot string) (*controlplane.GCDispositionProof, error) {
	var result struct {
		Proof *controlplane.GCDispositionProof `json:"proof"`
	}
	err := c.postJSON(ctx, "/v1/agents/"+url.PathEscape(c.Identity.ID)+"/gc-cleanup-proof", map[string]any{"candidate": candidate, "grant": g, "original_boot": originalBoot}, &result)
	return result.Proof, err
}
