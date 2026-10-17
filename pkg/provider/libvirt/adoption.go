package libvirt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"path/filepath"
)

func (s *Substrate) InspectAdoption(ctx context.Context, id string, spec substrate.NodeSpec) (substrate.AdoptionEvidence, error) {
	raw, err := virshQuery(ctx, "dumpxml", id)
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	if domain.UUID == "" || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return substrate.AdoptionEvidence{}, fmt.Errorf("libvirt adoption requires managed domain metadata")
	}
	sum := sha256.Sum256(raw)
	evidence, err := json.Marshal(struct {
		Version   int    `json:"version"`
		UUID      string `json:"uuid"`
		XMLSHA256 string `json:"xml_sha256"`
	}{1, domain.UUID, hex.EncodeToString(sum[:])})
	if err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	managed := domain.Metadata.Managed
	identity := substrate.OperationIdentity{ActionID: managed.ActionID, OwnerUID: managed.OwnerUID, TopologyUID: managed.TopologyUID, ResourceUID: managed.ResourceUID, Generation: managed.Generation}
	if err := identity.Validate(); err != nil {
		return substrate.AdoptionEvidence{}, err
	}
	return substrate.AdoptionEvidence{ManagedIdentity: &identity, Provider: subName, ExternalID: domain.UUID, Fingerprint: "sha256:" + hex.EncodeToString(sum[:]), State: evidence}, nil
}
func (s *Substrate) adoptionDomain(ctx context.Context, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	if err := id.Validate(); err != nil {
		return substrate.NodeHandle{}, err
	}
	if e.Provider != subName || e.ExternalID == "" || e.Fingerprint == "" {
		return substrate.NodeHandle{}, fmt.Errorf("invalid libvirt adoption evidence")
	}
	if _, err := uuid.Parse(e.ExternalID); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires immutable domain UUID")
	}
	raw, err := virshQuery(ctx, "dumpxml", e.ExternalID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != e.Fingerprint {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt domain changed since inspection")
	}
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return substrate.NodeHandle{}, err
	}
	if domain.UUID != e.ExternalID || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "ownership metadata missing", nil)
	}
	m := domain.Metadata.Managed
	if m.OwnerUID != id.OwnerUID || m.TopologyUID != id.TopologyUID || m.ResourceUID != id.ResourceUID || m.Generation != id.Generation {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt adoption identity conflict")
	}
	// Managed XML is also the recovery record. Returning only UUID/identity
	// loses disk, attachment and guest connection coordinates on cold restart.
	var hs HandleState
	if err := json.Unmarshal([]byte(m.State), &hs); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt adoption recovery state invalid: %w", err)
	}
	dir, err := s.managedVMDir(id, domain.Name)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if hs.Identity == nil || hs.Identity.Validate() != nil || !sameGeneration(*hs.Identity, id) || hs.DomainUUID != domain.UUID || hs.DomainName != domain.Name || hs.VMDir != dir || hs.DiskPath != filepath.Join(dir, "disk.qcow2") {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt adoption recovery identity conflict")
	}
	return managedNodeHandle(&hs), nil
}
func (s *Substrate) ApplyAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires operation identity")
	}
	return s.adoptionDomain(ctx, e, id)
}
func (s *Substrate) ObserveAdoption(ctx context.Context, e substrate.AdoptionEvidence) (substrate.NodeHandle, error) {
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.NodeHandle{}, fmt.Errorf("adoption requires operation identity")
	}
	return s.adoptionDomain(ctx, e, id)
}
