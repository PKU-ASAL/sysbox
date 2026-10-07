package libvirt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
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
	return substrate.AdoptionEvidence{Provider: subName, ExternalID: domain.UUID, Fingerprint: "sha256:" + hex.EncodeToString(sum[:]), State: raw}, nil
}
func (s *Substrate) adoptionDomain(ctx context.Context, e substrate.AdoptionEvidence, id substrate.OperationIdentity) (substrate.NodeHandle, error) {
	if e.Provider != subName || e.ExternalID == "" || e.Fingerprint == "" {
		return substrate.NodeHandle{}, fmt.Errorf("invalid libvirt adoption evidence")
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
	return substrate.NodeHandle{ID: domain.UUID, Generation: id.Generation, Provider: &HandleState{Identity: &id, DomainName: domain.Name, DomainUUID: domain.UUID}}, nil
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
