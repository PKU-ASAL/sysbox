package libvirt

import (
	"context"
	"encoding/xml"
	"fmt"
	"github.com/google/uuid"
)

// InspectFencedGC observes a definition, never destroys or undefines it. GC
// preserves external disks, so definition absence is the exact GC target scope.
func (s *Substrate) InspectFencedGC(ctx context.Context, id, owner, resource string, generation int64) (string, error) {
	if _, err := s.HostFenceScope(ctx); err != nil {
		return "", err
	}
	if _, err := uuid.Parse(id); err != nil || owner == "" || resource == "" || generation < 1 {
		return "", fmt.Errorf("invalid GC identity")
	}
	present, err := gcDomainPresent(ctx, id)
	if err != nil {
		return "", err
	}
	if !present {
		return "absent", nil
	}
	raw, err := virshQuery(ctx, "dumpxml", id)
	if err != nil {
		return "", err
	}
	var domain domainXML
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != id || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return "", fmt.Errorf("invalid GC domain evidence")
	}
	m := domain.Metadata.Managed
	if m.OwnerUID != owner || m.TopologyUID != owner || m.ResourceUID != resource || m.Generation != generation {
		return "", fmt.Errorf("GC domain identity changed")
	}
	return "present", nil
}
