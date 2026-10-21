package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
	"net/url"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
)

func (c AuthorityClient) AuthorizeAdoptionFence(ctx context.Context, id string) (controlplane.AdoptionDispositionProof, error) {
	var proof controlplane.AdoptionDispositionProof
	if !recoveryCommandID.MatchString(id) {
		return proof, fmt.Errorf("invalid fence ID")
	}
	err := c.postJSON(ctx, "/v1/agents/"+url.PathEscape(c.Identity.ID)+"/adoption-fences/"+url.PathEscape(id)+"/authorize", struct{}{}, &proof)
	return proof, err
}

func (c *AdoptionCoordinator) Disposition(ctx context.Context, id string) (controlplane.AdoptionDispositionProof, error) {
	var zero controlplane.AdoptionDispositionProof
	if c.AuthorizeFence == nil || (c.InspectFencedAdoption == nil && c.RecoverFencedAdoption == nil) || len(c.HostKey) != ed25519.PrivateKeySize {
		return zero, effectgate.ErrFenced
	}
	proof, err := c.AuthorizeFence(ctx, id)
	if err != nil {
		return zero, err
	}
	if proof.Request.ID != id || proof.Request.Grant.AgentID != c.AgentID || proof.Fence.NewBootID != c.HostBootID || controlplane.VerifyHostFenceReceipt(proof.Request, proof.Fence) != nil {
		return zero, effectgate.ErrFenced
	}
	digest, err := proof.Adoption.Digest()
	if err != nil || proof.Request.Grant.Operation != "adoption_apply" || proof.Request.Grant.Digest != digest {
		return zero, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	pub := c.HostKey.Public().(ed25519.PublicKey)
	prior, priorErr := gate.ReadAdoptionDisposition(ctx, proof.Request)
	if priorErr == nil {
		if controlplane.VerifyAdoptionDisposition(pub, prior) != nil {
			return zero, effectgate.ErrUncertain
		}
		return prior, nil
	}
	if !errors.Is(priorErr, effectgate.ErrNotFound) && !errors.Is(priorErr, effectgate.ErrUncertain) {
		return zero, priorErr
	}
	if len(proof.Signature) == 0 {
		var observation substrate.NodeObservation
		var private json.RawMessage
		if c.RecoverFencedAdoption != nil && proof.Adoption.Provider == "firecracker" {
			observation, private, err = c.RecoverFencedAdoption(ctx, proof, func(ctx context.Context) error {
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
			})
		} else if c.InspectFencedAdoption != nil {
			observation, private, err = c.InspectFencedAdoption(ctx, proof.Adoption)
		} else {
			return zero, effectgate.ErrFenced
		}
		if err != nil {
			return zero, err
		}
		proof.Observation = observation
		proof.Private = private
		proof.HostBootID = c.HostBootID
		proof.ObservedAt = time.Now().UTC()
		proof, err = controlplane.SignAdoptionDisposition(c.HostKey, proof)
		if err != nil {
			return zero, err
		}
	} else if err = controlplane.VerifyAdoptionDisposition(pub, proof); err != nil {
		return zero, err
	}
	// Recheck that the freeze still names this exact admitted fence before
	// changing the local journal. Resource inspection itself is read-only.
	if err = gate.FenceAdoption(ctx, proof, pub, func(ctx context.Context) error {
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
func (c *TaskClient) InspectAdoptionDisposition(ctx context.Context, id string) (controlplane.AdoptionDispositionProof, error) {
	response, err := c.call(ctx, Request{Operation: "adoption-disposition", AdoptionFenceID: id})
	if err != nil {
		return controlplane.AdoptionDispositionProof{}, err
	}
	if response.AdoptionDisposition == nil {
		return controlplane.AdoptionDispositionProof{}, fmt.Errorf("missing GC disposition proof")
	}
	return *response.AdoptionDisposition, nil
}
