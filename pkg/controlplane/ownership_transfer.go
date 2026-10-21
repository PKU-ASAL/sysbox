package controlplane

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/substrate"
)

// OwnershipTransferPlan is an explicit immutable intent to move one external
// object between owners. It is neither adoption permission nor stop evidence.
type OwnershipTransferPlan struct {
	Version        int                         `json:"version"`
	ID             string                      `json:"id"`
	SourceTopology string                      `json:"source_topology"`
	TargetTopology string                      `json:"target_topology"`
	SourceAddress  string                      `json:"source_address"`
	TargetAddress  string                      `json:"target_address"`
	AgentID        string                      `json:"agent_id"`
	Provider       string                      `json:"provider"`
	ExternalID     string                      `json:"external_id"`
	Source         substrate.OperationIdentity `json:"source"`
	Target         substrate.OperationIdentity `json:"target"`
	SourceSerial   int64                       `json:"source_serial"`
	TargetSerial   int64                       `json:"target_serial"`
	Fingerprint    string                      `json:"fingerprint"`
}

func (p OwnershipTransferPlan) Validate() error {
	if p.Version != 1 || !fenceName.MatchString(p.ID) || !fenceName.MatchString(p.SourceTopology) || !fenceName.MatchString(p.TargetTopology) || p.SourceTopology == p.TargetTopology || p.AgentID == "" || p.ExternalID == "" || p.SourceSerial < 0 || p.TargetSerial < 0 || p.Source.Validate() != nil || p.Target.Validate() != nil || p.Source.OwnerUID != p.Source.TopologyUID || p.Target.OwnerUID != p.Target.TopologyUID || p.Source.OwnerUID == p.Target.OwnerUID || p.Source.ResourceUID == p.Target.ResourceUID || p.Target.Generation <= p.Source.Generation || p.Target.ActionID != p.ID {
		return fmt.Errorf("invalid ownership transfer binding")
	}
	if p.Provider != "docker" && p.Provider != "libvirt" && p.Provider != "firecracker" {
		return fmt.Errorf("unsupported transfer provider")
	}
	if _, err := address.Parse(p.SourceAddress); err != nil {
		return err
	}
	if _, err := address.Parse(p.TargetAddress); err != nil {
		return err
	}
	if len(p.Fingerprint) != 71 || p.Fingerprint[:7] != "sha256:" || !validFenceDigest(p.Fingerprint[7:]) {
		return fmt.Errorf("invalid transfer fingerprint")
	}
	return nil
}
func (p OwnershipTransferPlan) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("sysbox.ownership-transfer-plan.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

type OwnershipRetirementProof struct {
	Plan              OwnershipTransferPlan `json:"plan"`
	PlanDigest        string                `json:"plan_digest"`
	SourceEpoch       uint64                `json:"source_epoch"`
	SourceStateDigest string                `json:"source_state_digest"`
	RetiredAt         time.Time             `json:"retired_at"`
	Signature         []byte                `json:"signature"`
}

func (p OwnershipRetirementProof) validate() error {
	digest, err := p.Plan.Digest()
	if err != nil {
		return err
	}
	if digest != p.PlanDigest || p.SourceEpoch == 0 || !validFenceDigest(p.SourceStateDigest) || p.RetiredAt.IsZero() {
		return fmt.Errorf("invalid ownership retirement proof")
	}
	return nil
}
func ownershipRetirementMessage(p OwnershipRetirementProof) ([]byte, error) {
	p.Signature = nil
	raw, err := json.Marshal(p)
	return append([]byte("sysbox.ownership-retirement.v1\x00"), raw...), err
}
func SignOwnershipRetirement(key ed25519.PrivateKey, p OwnershipRetirementProof) (OwnershipRetirementProof, error) {
	if len(key) != ed25519.PrivateKeySize {
		return OwnershipRetirementProof{}, fmt.Errorf("invalid retirement signing key")
	}
	if err := p.validate(); err != nil {
		return OwnershipRetirementProof{}, err
	}
	raw, err := ownershipRetirementMessage(p)
	if err != nil {
		return OwnershipRetirementProof{}, err
	}
	p.Signature = ed25519.Sign(key, raw)
	return p, nil
}
func VerifyOwnershipRetirement(key ed25519.PublicKey, p OwnershipRetirementProof) error {
	if err := p.validate(); err != nil {
		return err
	}
	raw, err := ownershipRetirementMessage(p)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, p.Signature) {
		return fmt.Errorf("invalid ownership retirement signature")
	}
	return nil
}
