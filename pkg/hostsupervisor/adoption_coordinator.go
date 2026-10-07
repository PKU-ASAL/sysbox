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
	"github.com/oslab/sysbox/pkg/substrate"
	"time"
)

type AdoptionRequest struct {
	Topology       string                     `json:"topology"`
	TopologyUID    string                     `json:"topology_uid"`
	Address        string                     `json:"address"`
	Provider       string                     `json:"provider"`
	ExternalID     string                     `json:"external_id"`
	ResourceUID    string                     `json:"resource_uid"`
	Generation     int64                      `json:"generation"`
	ExpectedSerial int64                      `json:"expected_serial"`
	Evidence       substrate.AdoptionEvidence `json:"evidence"`
}

func (a AdoptionRequest) Validate() error {
	if a.Topology == "" || a.TopologyUID == "" || a.Address == "" || a.Provider == "" || a.ExternalID == "" || a.ResourceUID == "" || a.Generation < 1 || a.ExpectedSerial < 0 {
		return fmt.Errorf("incomplete adoption request")
	}
	if a.Evidence.Provider != a.Provider || a.Evidence.ExternalID != a.ExternalID || a.Evidence.Fingerprint == "" {
		return fmt.Errorf("adoption inspection evidence mismatch")
	}
	return nil
}
func (a AdoptionRequest) Digest() (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type AdoptionCoordinator struct {
	Root                string
	IssuerKey           ed25519.PublicKey
	HostBootID, AgentID string
	Authorize           func(context.Context, controlplane.SignedExecutionGrant) error
}

func (c *AdoptionCoordinator) Apply(ctx context.Context, req AdoptionRequest, g controlplane.SignedExecutionGrant, effect func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	d, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if g.Grant.Operation != "adoption_apply" || g.Grant.Digest != d || g.Grant.Scope.TopologyUID != req.TopologyUID || g.Grant.Scope.ResourceUID != req.ResourceUID || g.Grant.Scope.Generation != req.Generation || g.Grant.TargetBootID != c.HostBootID {
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
	return gate.Execute(ctx, g, req.ExternalID, effect)
}
func (c *AdoptionCoordinator) Observe(ctx context.Context, req AdoptionRequest, g controlplane.SignedExecutionGrant) (json.RawMessage, error) {
	d, err := req.Digest()
	if err != nil {
		return nil, err
	}
	if d != g.Grant.Digest {
		return nil, effectgate.ErrFenced
	}
	gate := &effectgate.Gate{Root: c.Root, PublicKey: c.IssuerKey, BootID: c.HostBootID}
	return gate.ReadOutcome(ctx, g.Grant, req.ExternalID)
}
