package libvirt

import (
	"context"
	"encoding/xml"
	"fmt"
	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"strings"
)

// InspectAdoptionRecovery uses the immutable domain UUID, never a logical name.
// Fresh XML supplies the post-transfer fingerprint and preserved recovery state.
func (s *Substrate) InspectAdoptionRecovery(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeHandle, error) {
	evidence, err := s.InspectAdoption(ctx, r.ExternalID, substrate.NodeSpec{})
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	return s.ObserveAdoption(ctx, evidence)
}

func (s *Substrate) InspectAdoptionCancellation(ctx context.Context, r controlplane.AdoptionRequest) (substrate.NodeObservation, error) {
	zero := substrate.NodeObservation{}
	if _, err := uuid.Parse(r.ExternalID); err != nil {
		return zero, err
	}
	present, err := gcDomainPresent(ctx, r.ExternalID)
	if err != nil {
		return zero, err
	}
	if !present {
		return substrate.NodeObservation{ExternalID: r.ExternalID}, nil
	}
	raw, err := virshQuery(ctx, "dumpxml", r.ExternalID)
	if err != nil {
		return zero, err
	}
	var d domainXML
	if xml.Unmarshal(raw, &d) != nil || d.UUID != r.ExternalID {
		return zero, fmt.Errorf("domain immutable identity conflict")
	}
	status, err := virshQuery(ctx, "domstate", r.ExternalID)
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(string(status)) != "shut off" {
		return zero, fmt.Errorf("cancellation requires inactive domain")
	}
	observed := substrate.NodeObservation{Exists: true, ExternalID: r.ExternalID}
	if d.Metadata != nil && d.Metadata.Managed != nil {
		m := d.Metadata.Managed
		if m.TopologyUID != m.OwnerUID {
			return zero, fmt.Errorf("domain ownership conflict")
		}
		observed.OwnerUID, observed.ResourceUID, observed.Generation = m.OwnerUID, m.ResourceUID, m.Generation
	}
	if _, err = controlplane.AdoptionCancellationResolution(r, observed); err != nil {
		return zero, err
	}
	return observed, nil
}
